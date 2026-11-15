package cookist

import com.fasterxml.jackson.databind.JsonNode
import com.fasterxml.jackson.databind.node.ObjectNode
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import java.net.URI
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import java.nio.file.Files
import java.nio.file.Path
import java.util.UUID
import java.util.concurrent.CompletableFuture
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotEquals
import kotlin.test.assertTrue

class CoreTest {
    private lateinit var directory: Path
    private lateinit var db: Database
    private lateinit var server: CoreServer
    private val http = HttpClient.newHttpClient()
    private lateinit var customer: String
    private lateinit var sam: String
    private lateinit var merchant: String
    private lateinit var otherMerchant: String
    private lateinit var courier: String
    private lateinit var jordan: String

    @BeforeEach
    fun setup() {
        directory = Path.of("services", "core", "build", "test-data", UUID.randomUUID().toString())
        Files.createDirectories(directory)
        db = Database(directory.resolve("core.db"), Path.of("shared", "catalog.json"))
        start()
        customer = login("customer", "customer-1")
        sam = login("customer", "customer-2")
        merchant = login("merchant", "merchant-hearth")
        otherMerchant = login("merchant", "merchant-green")
        courier = login("courier", "courier-1")
        jordan = login("courier", "courier-2")
    }

    private fun start() { server = CoreServer(db, port = 0); server.start() }
    @AfterEach
    fun cleanup() {
        server.close()
        Files.walk(directory).use { paths -> paths.sorted(Comparator.reverseOrder()).forEach { Files.deleteIfExists(it) } }
    }

    private fun request(method: String, path: String, token: String? = null, body: Any? = null, expected: Int = 200): JsonNode {
        val raw = body?.let { json.writeValueAsString(it) }
        val (status, response) = raw(method, path, token, raw)
        assertEquals(expected, status, "$method $path: $response")
        return response
    }
    private fun raw(method: String, path: String, token: String?, body: String?): Pair<Int, JsonNode> {
        val b = HttpRequest.newBuilder(URI("http://127.0.0.1:${server.port}$path"))
            .header("Content-Type", "application/json")
        if (token != null) b.header("Authorization", "Bearer $token")
        b.method(method, if (body == null) HttpRequest.BodyPublishers.noBody() else HttpRequest.BodyPublishers.ofString(body))
        val response = http.send(b.build(), HttpResponse.BodyHandlers.ofString())
        assertTrue(response.headers().firstValue("Content-Type").orElse("").contains("application/json"))
        return response.statusCode() to json.readTree(response.body())
    }
    private fun login(role: String, actorId: String): String {
        val s = request("POST", "/api/sessions", body = obj("role" to role, "actorId" to actorId))
        assertEquals(setOf("token", "role", "actorId"), s.fieldNames().asSequence().toSet())
        assertEquals(role, s["role"].asText())
        return s["token"].asText()
    }
    private fun checkout(key: String = UUID.randomUUID().toString(), method: String = "demo_card", quantity: Int = 1): ObjectNode = obj(
        "storeId" to "hearth", "items" to listOf(obj("menuItemId" to "hearth-margherita", "quantity" to quantity)),
        "deliveryAddress" to "123 Local Street", "paymentMethod" to method, "idempotencyKey" to key
    )
    private fun place(body: JsonNode = checkout()): JsonNode = request("POST", "/api/orders", customer, body, 201)
    private fun status(id: String, token: String, state: String, expected: Int = 200): JsonNode =
        request("POST", "/api/orders/$id/status", token, obj("status" to state), expected)
    private fun ready(): String {
        val id = place()["id"].asText()
        status(id, merchant, "accepted"); status(id, merchant, "preparing"); status(id, merchant, "ready")
        return id
    }

    @Test
    fun `public catalog session and order shapes match contract`() {
        assertEquals("ok", request("GET", "/health")["status"].asText())
        val accounts = request("GET", "/api/demo-accounts")
        assertEquals(2, accounts["customers"].size())
        assertEquals("Alex", accounts["customers"][0]["name"].asText())
        assertTrue(accounts["customers"][0]["storeId"].isNull)
        val stores = request("GET", "/api/stores")
        assertEquals(stores.size(), accounts["merchants"].size())
        assertEquals(stores[0], request("GET", "/api/stores/hearth"))
        val o = place()
        assertEquals(setOf("id", "storeId", "storeName", "customerId", "customerName", "deliveryAddress", "items", "totals", "status", "payment", "courierId", "courierName", "location", "createdAt", "updatedAt", "events"), o.fieldNames().asSequence().toSet())
        assertTrue(o["location"].isNull && o["courierId"].isNull && o["courierName"].isNull)
        assertEquals(1695, o["totals"]["subtotalCents"].asInt())
        assertEquals(170, o["totals"]["taxCents"].asInt())
        assertEquals(2213, o["totals"]["totalCents"].asInt())
        assertEquals("simulated", o["payment"]["method"].asText())
        assertEquals("Alex", o["customerName"].asText())
    }

    @Test
    fun `sessions orders availability and idempotency survive restart`() {
        val b = checkout()
        val first = place(b)
        request("PATCH", "/api/stores/hearth/availability", merchant, obj("isOpen" to false))
        request("PATCH", "/api/menu-items/hearth-soda", merchant, obj("available" to false))
        server.close()
        db = Database(directory.resolve("core.db"), Path.of("shared", "catalog.json"))
        start()
        assertEquals(first, request("GET", "/api/orders/${first["id"].asText()}", customer))
        assertEquals(first, request("POST", "/api/orders", customer, b))
        assertFalse(request("GET", "/api/stores/hearth")["isOpen"].asBoolean())
        assertFalse(request("GET", "/api/stores/hearth")["menu"].last()["available"].asBoolean())
        val changed = b.deepCopy().put("deliveryAddress", "999 Different Street")
        request("POST", "/api/orders", customer, changed, 409)
        assertEquals(1, request("GET", "/api/orders", customer).size())
    }

    @Test
    fun `customer scoped keys replay normalized payload and reject changed quantities`() {
        val b = checkout()
        val first = place(b)
        assertEquals(first, request("POST", "/api/orders", customer, b.deepCopy().put("deliveryAddress", " 123 Local Street ")))
        val changed = checkout(b["idempotencyKey"].asText(), quantity = 2)
        request("POST", "/api/orders", customer, changed, 409)
        val second = request("POST", "/api/orders", sam, b, 201)
        assertNotEquals(first["id"], second["id"])
        status(first["id"].asText(), customer, "cancelled")
        assertEquals(first, request("POST", "/api/orders", customer, b))
    }

    @Test
    fun `changed ordered items conflict and replay ignores later catalog changes`() {
        val b = checkout().set<ObjectNode>("items", node(listOf(
            obj("menuItemId" to "hearth-margherita", "quantity" to 1),
            obj("menuItemId" to "hearth-soda", "quantity" to 2)
        )))
        val first = place(b)
        val reversed = b.deepCopy().set<ObjectNode>("items", node(b["items"].reversed()))
        request("POST", "/api/orders", customer, reversed, 409)
        request("PATCH", "/api/menu-items/hearth-soda", merchant, obj("available" to false))
        assertEquals(first, request("POST", "/api/orders", customer, b))
    }

    @Test
    fun `role and ownership isolation hide inaccessible orders`() {
        request("GET", "/api/orders", expected = 401)
        request("GET", "/api/orders", "not-a-session", expected = 401)
        val id = place()["id"].asText()
        for (t in listOf(sam, otherMerchant, courier)) {
            assertEquals(0, request("GET", "/api/orders", t).size())
            request("GET", "/api/orders/$id", t, expected = 404)
            status(id, t, "cancelled", 404)
            request("POST", "/api/orders/$id/location", t, obj("lat" to 0, "lng" to 0), 404)
        }
        request("POST", "/api/orders", merchant, checkout(), 403)
        request("POST", "/api/orders/$id/claim", customer, obj(), 403)
        request("PATCH", "/api/stores/hearth/availability", otherMerchant, obj("isOpen" to false), 403)
        request("PATCH", "/api/menu-items/hearth-soda", customer, obj("available" to false), 403)
        request("PATCH", "/api/menu-items/hearth-soda", otherMerchant, obj("available" to false), 403)
        assertEquals(1, request("GET", "/api/orders", merchant).size())
    }

    @Test
    fun `transitions refunds terminal states and courier location guards`() {
        var id = place()["id"].asText()
        status(id, merchant, "ready", 409)
        status(id, customer, "accepted", 409)
        val cancelled = status(id, customer, "cancelled")
        assertEquals("refunded", cancelled["payment"]["status"].asText())
        status(id, merchant, "accepted", 409)
        status(id, customer, "cancelled", 409)
        id = ready()
        status(id, courier, "picked_up", 403)
        status(id, customer, "cancelled", 409)
        request("POST", "/api/orders/$id/location", courier, obj("lat" to 47.6, "lng" to -122.3), 403)
        val claimed = request("POST", "/api/orders/$id/claim", courier, obj())
        assertEquals("Riley", claimed["courierName"].asText())
        request("POST", "/api/orders/$id/location", courier, obj("lat" to 91, "lng" to 0), 400)
        request("POST", "/api/orders/$id/location", courier, obj("lat" to "47", "lng" to 0), 400)
        val located = request("POST", "/api/orders/$id/location", courier, obj("lat" to 47.6, "lng" to -122.3))
        assertEquals(47.6, located["location"]["lat"].asDouble())
        assertTrue(located["location"]["updatedAt"].isTextual)
        status(id, courier, "delivered", 409)
        status(id, courier, "picked_up")
        status(id, merchant, "cancelled", 409)
        val delivered = status(id, courier, "delivered")
        assertEquals("captured", delivered["payment"]["status"].asText())
        status(id, courier, "delivered", 409)
        request("POST", "/api/orders/$id/location", courier, obj("lat" to 0, "lng" to 0), 409)
    }

    @Test
    fun `merchant cancellation refunds at each permitted stage including claimed ready`() {
        for (stage in listOf("pending", "accepted", "preparing", "ready")) {
            val id = place()["id"].asText()
            if (stage != "pending") status(id, merchant, "accepted")
            if (stage in listOf("preparing", "ready")) status(id, merchant, "preparing")
            if (stage == "ready") {
                status(id, merchant, "ready")
                request("POST", "/api/orders/$id/claim", courier, obj())
            }
            val cancelled = status(id, merchant, "cancelled")
            assertEquals("refunded", cancelled["payment"]["status"].asText())
            assertEquals("cancelled", cancelled["events"].last()["status"].asText())
        }
    }

    @Test
    fun `concurrent claims use atomic compare and set across database instances`() {
        val id = ready()
        assertEquals(1, request("GET", "/api/orders", jordan).size())
        val second = CoreServer(Database(directory.resolve("core.db"), Path.of("shared", "catalog.json")), port = 0)
        second.start()
        try {
            fun claim(port: Int, token: String) = CompletableFuture.supplyAsync {
                val r = HttpRequest.newBuilder(URI("http://127.0.0.1:$port/api/orders/$id/claim"))
                    .header("Content-Type", "application/json").header("Authorization", "Bearer $token")
                    .POST(HttpRequest.BodyPublishers.ofString("{}")).build()
                http.send(r, HttpResponse.BodyHandlers.ofString())
            }
            val a = claim(server.port, courier)
            val b = claim(second.port, jordan)
            val replies = listOf(a.join(), b.join())
            assertEquals(listOf(200, 409), replies.map { it.statusCode() }.sorted())
            val loser = if (replies[0].statusCode() == 409) courier else jordan
            request("GET", "/api/orders/$id", loser, expected = 404)
            assertEquals(0, request("GET", "/api/orders", loser).size())
            request("POST", "/api/orders/$id/claim", loser, obj(), 409)
            val winner = if (loser == courier) jordan else courier
            request("POST", "/api/orders/$id/claim", winner, obj(), 409)
        } finally { second.close() }
    }

    @Test
    fun `concurrent duplicate checkout produces one persisted order`() {
        val b = checkout()
        val tasks = (1..6).map { CompletableFuture.supplyAsync { raw("POST", "/api/orders", customer, b.toString()) } }
        val replies = tasks.map { it.join() }
        assertEquals(1, replies.count { it.first == 201 })
        assertEquals(5, replies.count { it.first == 200 })
        assertEquals(1, replies.map { it.second["id"].asText() }.distinct().size)
        assertEquals(1, request("GET", "/api/orders", customer).size())
    }

    @Test
    fun `decline is persisted without creating order and binds idempotency key`() {
        val b = checkout(method = "demo_decline")
        val declined = request("POST", "/api/orders", customer, b, 402)
        assertEquals("payment_declined", declined["error"]["code"].asText())
        assertEquals(declined, request("POST", "/api/orders", customer, b, 402))
        request("POST", "/api/orders", customer, b.deepCopy().put("paymentMethod", "demo_card"), 409)
        assertEquals(0, request("GET", "/api/orders", customer).size())
        server.close(); db = Database(directory.resolve("core.db"), Path.of("shared", "catalog.json")); start()
        assertEquals(declined, request("POST", "/api/orders", customer, b, 402))
    }

    @Test
    fun `closed store and unavailable item block checkout without consuming key`() {
        val b = checkout()
        request("PATCH", "/api/stores/hearth/availability", merchant, obj("isOpen" to false))
        request("POST", "/api/orders", customer, b, 409)
        request("PATCH", "/api/stores/hearth/availability", merchant, obj("isOpen" to true))
        request("PATCH", "/api/menu-items/hearth-margherita", merchant, obj("available" to false))
        request("POST", "/api/orders", customer, b, 409)
        assertEquals(0, request("GET", "/api/orders", customer).size())
        request("PATCH", "/api/menu-items/hearth-margherita", merchant, obj("available" to true))
        place(b)
    }

    @Test
    fun `strict bounded json and checkout validation use standard errors`() {
        for (quantity in listOf(0, -1, 21)) request("POST", "/api/orders", customer, checkout(quantity = quantity), 400)
        val b = checkout()
        val invalids = listOf(
            b.deepCopy().put("deliveryAddress", "tiny"),
            b.deepCopy().put("deliveryAddress", "a".repeat(301)),
            b.deepCopy().put("idempotencyKey", "short"),
            b.deepCopy().put("paymentMethod", "real_card"),
            b.deepCopy().put("priceCents", 1),
            b.deepCopy().set<ObjectNode>("items", node(emptyList<String>())),
            b.deepCopy().set<ObjectNode>("items", node(List(31) { obj("menuItemId" to "x-$it", "quantity" to 1) })),
            b.deepCopy().set<ObjectNode>("items", node(listOf(obj("menuItemId" to "hearth-soda", "quantity" to 1), obj("menuItemId" to "hearth-soda", "quantity" to 1)))),
            b.deepCopy().set<ObjectNode>("items", node(listOf(obj("menuItemId" to "green-tea", "quantity" to 1)))),
            b.deepCopy().set<ObjectNode>("items", node(listOf(obj("menuItemId" to "hearth-soda", "quantity" to 1.5)))),
            b.deepCopy().set<ObjectNode>("items", node(listOf(obj("menuItemId" to "hearth-soda", "quantity" to "1")))),
            b.deepCopy().set<ObjectNode>("items", node(listOf(obj("menuItemId" to "hearth-soda", "quantity" to 1, "unitPriceCents" to 1))))
        )
        for (invalid in invalids) {
            val err = request("POST", "/api/orders", customer, invalid, 400)
            assertEquals(setOf("error"), err.fieldNames().asSequence().toSet())
            assertEquals(setOf("code", "message"), err["error"].fieldNames().asSequence().toSet())
        }
        for (invalid in listOf("{", "null", "[]", "{}{}", """{"role":"customer","role":"courier","actorId":"courier-1"}""", "[".repeat(40))) {
            assertEquals(400, raw("POST", "/api/sessions", null, invalid).first)
        }
        assertEquals(413, raw("POST", "/api/sessions", null, " ".repeat(32769)).first)
        request("POST", "/api/sessions", body = obj("role" to "merchant", "actorId" to "customer-1"), expected = 400)
        request("PATCH", "/api/stores/hearth/availability", merchant, obj("isOpen" to "false"), 400)
        request("GET", "/missing", expected = 404)
        request("GET", "/api/stores/missing", expected = 404)
        request("DELETE", "/api/stores", expected = 405)
        assertEquals(0, request("GET", "/api/orders", customer).size())
    }
}
