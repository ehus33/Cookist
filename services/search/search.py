"""Cookist's local catalog search. No third-party service or packages required."""

import json
import logging
import math
import os
import re
import unicodedata
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlsplit
from urllib.request import urlopen

LOG = logging.getLogger("cookist.search")


class InvalidQuery(ValueError):
    pass


def normalize(value: str) -> str:
    return "".join(
        char for char in unicodedata.normalize("NFKD", value.casefold())
        if not unicodedata.combining(char)
    )


def parse_filters(query_string: str) -> dict:
    try:
        params = parse_qs(query_string, keep_blank_values=True, max_num_fields=12)
    except ValueError as exc:
        raise InvalidQuery("Too many query parameters.") from exc
    allowed = {"q", "cuisine", "sort", "maxFeeCents", "openOnly", "limit"}
    if set(params) - allowed or any(len(values) != 1 for values in params.values()):
        raise InvalidQuery("Unknown or repeated search parameter.")
    values = {key: items[0] for key, items in params.items()}
    query = values.get("q", "").strip()
    cuisine = values.get("cuisine", "").strip()
    if len(query) > 100 or len(cuisine) > 80:
        raise InvalidQuery("Search text is too long.")
    sort = values.get("sort", "relevance")
    if sort not in {"relevance", "rating", "eta", "fee"}:
        raise InvalidQuery("Choose relevance, rating, eta, or fee sorting.")
    open_only = values.get("openOnly", "false")
    if open_only not in {"true", "false"}:
        raise InvalidQuery("openOnly must be true or false.")

    def integer(name: str, default: int, maximum: int, minimum: int = 0) -> int:
        text = values.get(name, str(default))
        if not re.fullmatch(r"[0-9]{1,9}", text):
            raise InvalidQuery(f"{name} must be a nonnegative integer.")
        number = int(text)
        if not minimum <= number <= maximum:
            raise InvalidQuery(f"{name} must be between {minimum} and {maximum}.")
        return number

    return {
        "query": query, "cuisine": cuisine, "sort": sort,
        "max_fee": integer("maxFeeCents", 100000, 100000),
        "open_only": open_only == "true", "limit": integer("limit", 20, 50, 1),
    }


def search_catalog(stores: list[dict], filters: dict) -> dict:
    query = filters["query"]
    tokens = normalize(query).split()
    ranked = []
    for store in stores:
        if filters["open_only"] and not store["isOpen"]:
            continue
        if filters["cuisine"] and normalize(store["cuisine"]) != normalize(filters["cuisine"]):
            continue
        if store["deliveryFeeCents"] > filters["max_fee"]:
            continue
        name = normalize(store["name"])
        tags = normalize(" ".join([store["cuisine"], *store["tags"]]))
        menu = normalize(" ".join(
            item["name"] + " " + item["description"]
            for item in store["menu"] if item["available"]
        ))
        if not all(token in name or token in tags or token in menu for token in tokens):
            continue
        score = sum(8 * (token in name) + 4 * (token in tags) + (token in menu) for token in tokens)
        if tokens and name.startswith(normalize(query)):
            score += 12
        ranked.append((score, store))
    sort = filters["sort"]
    if sort == "rating":
        ranked.sort(key=lambda pair: (-pair[1]["rating"], pair[1]["etaMinutes"], pair[1]["id"]))
    elif sort == "eta":
        ranked.sort(key=lambda pair: (pair[1]["etaMinutes"], -pair[1]["rating"], pair[1]["id"]))
    elif sort == "fee":
        ranked.sort(key=lambda pair: (pair[1]["deliveryFeeCents"], -pair[1]["rating"], pair[1]["id"]))
    else:
        ranked.sort(key=lambda pair: (-pair[0], -pair[1]["rating"], pair[1]["id"]))
    matches = [store for _, store in ranked[:filters["limit"]]]
    suggestions = []
    if tokens:
        seen = set()
        for store in matches:
            for term in [store["cuisine"], *store["tags"]]:
                text = term.casefold()
                if all(token in normalize(text) for token in tokens) and text not in seen:
                    suggestions.append({"type": "query", "text": text, "storeId": None})
                    seen.add(text)
                    if len(suggestions) == 2:
                        break
            if len(suggestions) == 2:
                break
        suggestions.extend(
            {"type": "store", "text": store["name"], "storeId": store["id"]}
            for store in matches[:4]
        )
    return {"query": query, "suggestions": suggestions, "stores": matches}


def load_catalog(core_url: str) -> list[dict]:
    with urlopen(core_url + "/api/stores", timeout=4) as response:
        raw = response.read(2_000_001)
        if len(raw) > 2_000_000:
            raise ValueError("Catalog exceeded the local search size limit.")
        data = json.loads(raw)
    if not isinstance(data, list):
        raise ValueError("Core returned a non-list catalog.")
    # Validate the fields search consumes; malformed upstream data must be an error.
    for store in data:
        for key in ("id", "name", "cuisine"):
            if not isinstance(store[key], str):
                raise ValueError("Invalid catalog text field.")
        if not isinstance(store["isOpen"], bool):
            raise ValueError("Invalid store availability.")
        if not isinstance(store["tags"], list) or not all(isinstance(tag, str) for tag in store["tags"]):
            raise ValueError("Invalid catalog tags.")
        for key in ("deliveryFeeCents", "etaMinutes", "rating"):
            value = store[key]
            if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
                raise ValueError("Invalid numeric catalog field.")
        if not isinstance(store["menu"], list):
            raise ValueError("Invalid catalog menu.")
        for item in store["menu"]:
            if not isinstance(item["available"], bool) or not all(
                isinstance(item[key], str) for key in ("name", "description")
            ):
                raise ValueError("Invalid catalog item.")
    return data


class Handler(BaseHTTPRequestHandler):
    server_version = "CookistSearch/1"

    def send_json(self, status: int, data: object) -> None:
        body = json.dumps(data, ensure_ascii=False, allow_nan=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            LOG.warning("Client disconnected before the response body could be sent.")

    def do_GET(self) -> None:
        if len(self.path) > 8192:
            self.send_json(414, {"error": {"code": "uri_too_long", "message": "Search URL is too long."}})
            return
        url = urlsplit(self.path)
        health_check = url.path == "/health"
        if not health_check and url.path != "/api/search":
            self.send_json(404, {"error": {"code": "not_found", "message": "Route not found."}})
            return
        try:
            filters = None if health_check else parse_filters(url.query)
        except InvalidQuery as exc:
            self.send_json(400, {"error": {"code": "invalid_query", "message": str(exc)}})
            return
        try:
            catalog = load_catalog(self.server.core_url)
            result = {"status": "ok"} if health_check else search_catalog(catalog, filters)
        except (HTTPError, URLError, TimeoutError, OSError, ValueError, KeyError, TypeError) as exc:
            LOG.error("Catalog search failed: %s", exc)
            self.send_json(503 if health_check else 502, {"error": {"code": "catalog_unavailable", "message": "Catalog is unavailable. Try again shortly."}})
            return
        self.send_json(200, result)

    def log_message(self, fmt: str, *args: object) -> None:
        # Do not log query strings or delivery context.
        LOG.info("HTTP response sent to %s", self.client_address[0])


class SearchServer(ThreadingHTTPServer):
    def __init__(self, address: tuple[str, int], core_url: str):
        self.core_url = core_url.rstrip("/")
        super().__init__(address, Handler)


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO)
    server = SearchServer(
        (os.environ.get("HOST", "127.0.0.1"), int(os.environ.get("PORT", "8082"))),
        os.environ.get("CORE_URL", "http://127.0.0.1:8081"),
    )
    LOG.info("Cookist search listening on %s:%s", *server.server_address)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
