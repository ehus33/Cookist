"""Exercise the real gateway -> search/core -> SQLite delivery workflow."""

import json
import os
import uuid
from urllib.error import HTTPError
from urllib.request import Request, urlopen

BASE = os.environ.get("COOKIST_URL", "http://127.0.0.1:8080").rstrip("/")


def request(method, path, body=None, token=None, expected=(200,)):
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urlopen(req, timeout=15) as response:
            status, result = response.status, json.load(response)
    except HTTPError as error:
        status, result = error.code, json.load(error)
    assert status in expected, f"{method} {path}: expected {expected}, got {status}: {result}"
    if status >= 400:
        assert isinstance(result["error"]["message"], str), result
    return result


def session(role, actor_id):
    return request("POST", "/api/sessions", {"role": role, "actorId": actor_id})["token"]


def main():
    assert request("GET", "/health")["status"] == "ok"
    accounts = request("GET", "/api/demo-accounts")
    assert len(accounts["merchants"]) == 6
    customer = session("customer", "customer-1")
    other_customer = session("customer", "customer-2")
    merchant = session("merchant", "merchant-hearth")
    other_merchant = session("merchant", "merchant-green")
    courier = session("courier", "courier-1")
    other_courier = session("courier", "courier-2")
    request("GET", "/api/orders", expected=(401,))
    request("POST", "/api/sessions", {"role": "merchant", "actorId": "customer-1"}, expected=(400, 401, 403, 404))
    result = request("GET", "/api/search?q=piz")
    assert any(store["id"] == "hearth" for store in result["stores"])
    assert {"store", "query"}.issubset({item["type"] for item in result["suggestions"]})
    request("GET", "/api/search?sort=bogus", expected=(400,))
    store = request("GET", "/api/stores/hearth")
    item = next(item for item in store["menu"] if item["id"] == "hearth-margherita")
    original_open = store["isOpen"]
    original_available = item["available"]
    try:
        request("PATCH", "/api/stores/hearth/availability", {"isOpen": True}, merchant)
        request("PATCH", "/api/menu-items/hearth-margherita", {"available": True}, merchant)
        body = {
            "storeId": "hearth", "items": [{"menuItemId": "hearth-margherita", "quantity": 2}],
            "deliveryAddress": "1000 4th Ave Seattle WA 98104 (automated local test)",
            "paymentMethod": "demo_card", "idempotencyKey": str(uuid.uuid4()),
        }
        request("POST", "/api/orders", body, merchant, (403,))
        request("POST", "/api/orders", {**body, "paymentMethod": "demo_decline", "idempotencyKey": str(uuid.uuid4())}, customer, (402,))
        request("POST", "/api/orders", {**body, "idempotencyKey": str(uuid.uuid4()), "items": [{"menuItemId": "hearth-margherita", "quantity": 0}]}, customer, (400,))
        order = request("POST", "/api/orders", body, customer, (201,))
        order_id = order["id"]
        replay = request("POST", "/api/orders", body, customer)
        assert replay["id"] == order_id, "Idempotent replay created another order"
        request("POST", "/api/orders", {**body, "deliveryAddress": "Changed test address, Seattle WA"}, customer, (409,))
        subtotal = item["priceCents"] * 2
        assert order["totals"] == {
            "subtotalCents": subtotal, "taxCents": (subtotal + 5) // 10,
            "deliveryFeeCents": store["deliveryFeeCents"], "serviceFeeCents": 149,
            "totalCents": subtotal + (subtotal + 5) // 10 + store["deliveryFeeCents"] + 149,
        }, order["totals"]
        assert order["payment"]["method"] == "simulated"
        path = "/api/orders/" + order_id
        request("GET", path, token=other_customer, expected=(403, 404))
        assert all(row["id"] != order_id for row in request("GET", "/api/orders", token=other_customer))
        request("POST", path + "/status", {"status": "accepted"}, other_merchant, (403, 404))
        request("POST", path + "/status", {"status": "ready"}, merchant, (409,))
        request("POST", path + "/status", {"status": "accepted"}, merchant)
        request("POST", path + "/status", {"status": "cancelled"}, customer, (409,))
        request("POST", path + "/status", {"status": "preparing"}, merchant)
        request("POST", path + "/status", {"status": "ready"}, merchant)
        jobs = request("GET", "/api/orders", token=courier)
        assert any(row["id"] == order_id for row in jobs), "Ready order missing from courier jobs"
        request("POST", path + "/status", {"status": "picked_up"}, courier, (403, 409))
        claimed = request("POST", path + "/claim", {}, courier)
        assert claimed["courierId"] == "courier-1"
        request("POST", path + "/claim", {}, other_courier, (409,))
        request("POST", path + "/location", {"lat": 91, "lng": 0}, courier, (400,))
        request("POST", path + "/location", {"lat": 47.6072, "lng": -122.3345}, courier)
        tracking = request("GET", path, token=customer)
        assert tracking["location"]["lat"] == 47.6072
        request("POST", path + "/status", {"status": "picked_up"}, courier)
        completed = request("POST", path + "/status", {"status": "delivered"}, courier)
        assert completed["status"] == "delivered" and len(completed["events"]) >= 6
        request("POST", path + "/status", {"status": "cancelled"}, merchant, (409,))
        second = request("POST", "/api/orders", {**body, "idempotencyKey": str(uuid.uuid4())}, customer, (201,))
        cancelled = request("POST", f"/api/orders/{second['id']}/status", {"status": "cancelled"}, customer)
        assert cancelled["payment"]["status"] == "refunded"
        request("PATCH", "/api/menu-items/hearth-margherita", {"available": False}, merchant)
        request("POST", "/api/orders", {**body, "idempotencyKey": str(uuid.uuid4())}, customer, (400, 409))
        request("PATCH", "/api/menu-items/hearth-margherita", {"available": True}, merchant)
        request("PATCH", "/api/stores/hearth/availability", {"isOpen": False}, merchant)
        request("POST", "/api/orders", {**body, "idempotencyKey": str(uuid.uuid4())}, customer, (400, 409))
        result = request("GET", "/api/search?q=pizza&openOnly=true")
        assert not any(row["id"] == "hearth" for row in result["stores"]), "Search did not honor fresh availability"
    finally:
        request("PATCH", "/api/menu-items/hearth-margherita", {"available": original_available}, merchant)
        request("PATCH", "/api/stores/hearth/availability", {"isOpen": original_open}, merchant)
    print("PASS: real search, checkout, idempotency, role isolation, merchant flow, courier claim, tracking, delivery, refunds, and availability.")


if __name__ == "__main__":
    main()
