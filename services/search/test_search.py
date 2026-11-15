import json
import threading
import unittest
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import urlopen
from unittest.mock import patch

from search import InvalidQuery, SearchServer, parse_filters, search_catalog

CATALOG = json.loads((Path(__file__).resolve().parents[2] / "shared" / "catalog.json").read_text())


class SearchTests(unittest.TestCase):
    def test_prefix_and_menu_search_with_mixed_suggestions(self):
        result = search_catalog(CATALOG, parse_filters("q=piz"))
        self.assertEqual(["hearth"], [store["id"] for store in result["stores"]])
        self.assertEqual({"query", "store"}, {item["type"] for item in result["suggestions"]})
        self.assertEqual("nori", search_catalog(CATALOG, parse_filters("q=miso"))["stores"][0]["id"])

    def test_combined_filters_and_sorting(self):
        result = search_catalog(CATALOG, parse_filters("maxFeeCents=199&sort=eta&openOnly=true"))
        self.assertEqual(["taco", "green", "sweet", "hearth"], [store["id"] for store in result["stores"]])
        self.assertEqual([], search_catalog(CATALOG, parse_filters("q=pizza&cuisine=Bakery"))["stores"])

    def test_closed_and_unavailable_data_changes_are_respected(self):
        stores = json.loads(json.dumps(CATALOG))
        stores[0]["isOpen"] = False
        self.assertEqual([], search_catalog(stores, parse_filters("q=pizza&openOnly=true"))["stores"])
        stores[3]["menu"][1]["available"] = False
        self.assertEqual([], search_catalog(stores, parse_filters("q=miso"))["stores"])

    def test_no_matches_and_case_normalization(self):
        self.assertEqual([], search_catalog(CATALOG, parse_filters("q=not-a-food"))["suggestions"])
        self.assertEqual("hearth", search_catalog(CATALOG, parse_filters("q=PIZZA"))["stores"][0]["id"])

    def test_rejects_bad_filters(self):
        for query in ("q=" + "a" * 101, "sort=oops", "limit=0", "limit=51", "openOnly=yes",
                      "maxFeeCents=-1", "q=a&q=b", "unexpected=1"):
            with self.subTest(query=query), self.assertRaises(InvalidQuery):
                parse_filters(query)

    def test_upstream_failure_is_502_not_empty_success(self):
        server = SearchServer(("127.0.0.1", 0), "http://127.0.0.1:1")
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with patch("search.load_catalog", side_effect=ValueError("broken catalog")):
                for path, status in (("/api/search?q=pizza", 502), ("/health", 503)):
                    with self.assertRaises(HTTPError) as caught:
                        urlopen(f"http://127.0.0.1:{server.server_port}{path}")
                    self.assertEqual(status, caught.exception.code)
                    self.assertEqual("catalog_unavailable", json.load(caught.exception)["error"]["code"])
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    unittest.main()
