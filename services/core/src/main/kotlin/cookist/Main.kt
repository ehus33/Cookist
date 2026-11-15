package cookist

import com.fasterxml.jackson.core.JsonFactory
import com.fasterxml.jackson.core.StreamReadConstraints
import com.fasterxml.jackson.core.StreamReadFeature
import com.fasterxml.jackson.databind.DeserializationFeature
import com.fasterxml.jackson.databind.JsonNode
import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.databind.node.ObjectNode
import com.sun.net.httpserver.HttpExchange
import com.sun.net.httpserver.HttpServer
import java.net.InetSocketAddress
import java.nio.file.Files
import java.nio.file.Path
import java.sql.Connection
import java.sql.DriverManager
import java.time.Instant
import java.util.UUID
import java.util.concurrent.Executors

internal val json = ObjectMapper(
    JsonFactory.builder()
        .streamReadConstraints(StreamReadConstraints.builder().maxNestingDepth(32).maxStringLength(10000).maxNumberLength(30).build())
        .enable(StreamReadFeature.STRICT_DUPLICATE_DETECTION).build()
).enable(DeserializationFeature.FAIL_ON_TRAILING_TOKENS)

internal fun node(value: Any?): JsonNode = json.valueToTree(value)
internal fun obj(vararg pairs: Pair<String, Any?>): ObjectNode = node(linkedMapOf(*pairs)) as ObjectNode
private fun now() = Instant.now().toString()
private fun id() = UUID.randomUUID().toString()
internal class ApiError(val status: Int, val code: String, override val message: String) : RuntimeException(message)
private fun fail(status: Int, code: String, message: String): Nothing = throw ApiError(status, code, message)
private fun requireInput(valid: Boolean, message: String) { if (!valid) fail(400, "invalid_input", message) }

internal fun JsonNode.fieldsExactly(vararg names: String) {
    requireInput(isObject && fieldNames().asSequence().toSet() == names.toSet(), "Expected fields: ${names.joinToString()}")
}
private fun JsonNode.text(name: String, min: Int = 1, max: Int = 100, trim: Boolean = false): String {
    val value = get(name)
    requireInput(value != null && value.isTextual, "$name must be a string")
    val text = if (trim) value.asText().trim() else value.asText()
    requireInput(text.length in min..max, "$name must contain $min..$max characters")
    return text
}
private fun JsonNode.bool(name: String): Boolean {
    requireInput(get(name)?.isBoolean == true, "$name must be boolean")
    return get(name).booleanValue()
}

class Database(private val path: Path, catalog: Path) {
    init {
        path.toAbsolutePath().parent?.let { Files.createDirectories(it) }
        Class.forName("org.sqlite.JDBC")
        connection().use { c ->
            c.createStatement().use { s ->
                s.execute("PRAGMA journal_mode=WAL")
                s.execute("CREATE TABLE IF NOT EXISTS stores (id TEXT PRIMARY KEY, body TEXT NOT NULL)")
                s.execute("CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY, body TEXT NOT NULL)")
                s.execute("CREATE TABLE IF NOT EXISTS orders (id TEXT PRIMARY KEY, body TEXT NOT NULL)")
                s.execute("CREATE TABLE IF NOT EXISTS idempotency (actor TEXT NOT NULL, key TEXT NOT NULL, payload TEXT NOT NULL, status INTEGER NOT NULL, body TEXT NOT NULL, PRIMARY KEY(actor,key))")
            }
        }
        val seed = json.readTree(catalog.toFile())
        require(seed.isArray && seed.size() > 0) { "Catalog must be a non-empty array" }
        val storeIds = mutableSetOf<String>()
        val menuIds = mutableSetOf<String>()
        seed.forEach { store ->
            require(storeIds.add(store["id"].asText())) { "Duplicate store ID" }
            require(store["deliveryFeeCents"].canConvertToInt() && store["deliveryFeeCents"].asInt() >= 0)
            store["menu"].forEach {
                require(menuIds.add(it["id"].asText())) { "Duplicate menu item ID" }
                require(it["priceCents"].canConvertToInt() && it["priceCents"].asInt() in 0..1_000_000)
            }
        }
        write { c ->
            seed.forEach { store ->
                c.prepareStatement("INSERT OR IGNORE INTO stores(id,body) VALUES (?,?)").use {
                    it.setString(1, store["id"].asText()); it.setString(2, store.toString()); it.executeUpdate()
                }
            }
        }
    }

    private fun connection(): Connection = DriverManager.getConnection("jdbc:sqlite:${path.toAbsolutePath()}").also {
        it.createStatement().use { s -> s.execute("PRAGMA busy_timeout=10000") }
    }
    internal fun <T> read(block: (Connection) -> T): T = connection().use(block)
    internal fun <T> write(block: (Connection) -> T): T = connection().use { c ->
        c.createStatement().use { it.execute("BEGIN IMMEDIATE") }
        try {
            val result = block(c)
            c.createStatement().use { it.execute("COMMIT") }
            result
        } catch (e: Throwable) {
            c.createStatement().use { it.execute("ROLLBACK") }
            throw e
        }
    }
}

private fun Connection.all(table: String): List<JsonNode> =
    createStatement().use { s -> s.executeQuery("SELECT body FROM $table ORDER BY rowid").use { r ->
        buildList { while (r.next()) add(json.readTree(r.getString(1))) }
    } }
private fun Connection.find(table: String, key: String, column: String = "id"): ObjectNode? =
    prepareStatement("SELECT body FROM $table WHERE $column=?").use { s ->
        s.setString(1, key)
        s.executeQuery().use { if (it.next()) json.readTree(it.getString(1)) as ObjectNode else null }
    }
private fun Connection.save(table: String, key: String, body: JsonNode, column: String = "id") {
    prepareStatement("INSERT INTO $table($column,body) VALUES (?,?) ON CONFLICT($column) DO UPDATE SET body=excluded.body").use {
        it.setString(1, key); it.setString(2, body.toString()); it.executeUpdate()
    }
}

class CoreServer(private val db: Database, host: String = "127.0.0.1", port: Int = 8081) : AutoCloseable {
    init {
        // Complete database reads and serializer initialization before accepting requests.
        db.read { c ->
            json.writeValueAsBytes(catalog(c))
            json.writeValueAsBytes(accounts(c))
        }
        json.writeValueAsBytes(obj("status" to "ok"))
    }

    private val executor = Executors.newFixedThreadPool(16)
    private val server = HttpServer.create(InetSocketAddress(host, port), 64).apply {
        executor = this@CoreServer.executor
        createContext("/") { exchange -> handle(exchange) }
    }
    val port: Int get() = server.address.port
    fun start() = server.start()
    override fun close() { server.stop(0); executor.shutdownNow() }

    private fun catalog(c: Connection): JsonNode = node(c.all("stores"))

    private fun accounts(c: Connection): ObjectNode = obj(
        "customers" to listOf(obj("id" to "customer-1", "name" to "Alex", "storeId" to null), obj("id" to "customer-2", "name" to "Sam", "storeId" to null)),
        "merchants" to c.all("stores").map { obj("id" to "merchant-${it["id"].asText()}", "name" to "${it["name"].asText()} merchant", "storeId" to it["id"].asText()) },
        "couriers" to listOf(obj("id" to "courier-1", "name" to "Riley", "storeId" to null), obj("id" to "courier-2", "name" to "Jordan", "storeId" to null))
    )
    private fun actor(c: Connection, session: JsonNode): JsonNode =
        accounts(c)[session["role"].asText() + "s"].first { it["id"] == session["actorId"] }
    private fun session(c: Connection, x: HttpExchange): ObjectNode {
        val auth = x.requestHeaders.getFirst("Authorization") ?: fail(401, "unauthorized", "A demo session is required")
        if (!auth.startsWith("Bearer ") || auth.length > 200) fail(401, "unauthorized", "Invalid demo session")
        return c.find("sessions", auth.removePrefix("Bearer "), "token") ?: fail(401, "unauthorized", "Invalid demo session")
    }
    private fun role(s: JsonNode, expected: String) {
        if (s["role"].asText() != expected) fail(403, "forbidden", "This action requires the $expected role")
    }
    private fun visible(order: JsonNode, s: JsonNode): Boolean = when (s["role"].asText()) {
        "customer" -> order["customerId"] == s["actorId"]
        "merchant" -> s["actorId"].asText() == "merchant-${order["storeId"].asText()}"
        "courier" -> order["courierId"] == s["actorId"] || (order["status"].asText() == "ready" && order["courierId"].isNull)
        else -> false
    }
    private fun order(c: Connection, key: String, s: JsonNode): ObjectNode {
        val found = c.find("orders", key)
        if (found == null || !visible(found, s)) fail(404, "not_found", "Order not found")
        return found
    }
    private fun store(c: Connection, key: String): ObjectNode = c.find("stores", key) ?: fail(404, "not_found", "Store not found")

    private fun body(x: HttpExchange): JsonNode {
        val type = x.requestHeaders.getFirst("Content-Type")?.substringBefore(';')?.trim()
        if (!"application/json".equals(type, ignoreCase = true)) fail(415, "unsupported_media_type", "Use application/json")
        val length = x.requestHeaders.getFirst("Content-Length")?.toLongOrNull()
        if (length != null && length > 32768) fail(413, "payload_too_large", "JSON body is limited to 32768 bytes")
        val bytes = x.requestBody.readNBytes(32769)
        if (bytes.size > 32768) fail(413, "payload_too_large", "JSON body is limited to 32768 bytes")
        return try {
            json.readTree(bytes)?.also { requireInput(it.isObject, "Body must be a JSON object") }
                ?: fail(400, "invalid_json", "JSON body is required")
        } catch (e: ApiError) { throw e }
        catch (_: Exception) { fail(400, "invalid_json", "Invalid JSON body") }
    }

    private fun handle(x: HttpExchange) {
        try {
            requireInput(x.requestURI.rawPath.length <= 2048, "Path is too long")
            val (status, response) = dispatch(x)
            respond(x, status, response)
        } catch (e: ApiError) {
            respond(x, e.status, obj("error" to obj("code" to e.code, "message" to e.message)))
        } catch (e: Exception) {
            System.err.println("Core request failed: ${e.javaClass.simpleName}")
            respond(x, 500, obj("error" to obj("code" to "internal_error", "message" to "An internal error occurred")))
        } finally { x.close() }
    }
    private fun respond(x: HttpExchange, status: Int, value: JsonNode) {
        val bytes = json.writeValueAsBytes(value)
        x.responseHeaders.set("Content-Type", "application/json; charset=utf-8")
        x.responseHeaders.set("Cache-Control", "no-store")
        x.responseHeaders.set("X-Content-Type-Options", "nosniff")
        x.sendResponseHeaders(status, bytes.size.toLong())
        x.responseBody.write(bytes)
    }
    private fun method(x: HttpExchange, expected: String) {
        if (x.requestMethod != expected) {
            x.responseHeaders.set("Allow", expected)
            fail(405, "method_not_allowed", "Use $expected")
        }
    }

    private fun dispatch(x: HttpExchange): Pair<Int, JsonNode> {
        val parts = x.requestURI.path.split('/').drop(1)
        val path = x.requestURI.path
        if (path == "/health") {
            method(x, "GET")
            return 200 to obj("status" to "ok")
        }
        if (path == "/api/demo-accounts") {
            method(x, "GET")
            return 200 to db.read { accounts(it) }
        }
        if (path == "/api/sessions") {
            method(x, "POST")
            val b = body(x); b.fieldsExactly("role", "actorId")
            val r = b.text("role"); val a = b.text("actorId")
            return 200 to db.write { c ->
                requireInput(r in setOf("customer", "merchant", "courier"), "Unknown role")
                requireInput(accounts(c)[r + "s"].any { it["id"].asText() == a }, "Unknown actor for role")
                obj("token" to id(), "role" to r, "actorId" to a).also { c.save("sessions", it["token"].asText(), it, "token") }
            }
        }
        if (path == "/api/stores") {
            method(x, "GET")
            return 200 to db.read { catalog(it) }
        }
        if (parts.size == 3 && parts.take(2) == listOf("api", "stores")) {
            method(x, "GET")
            return 200 to db.read { store(it, parts[2]) }
        }
        if ((parts.size == 4 && parts.take(2) == listOf("api", "stores") && parts[3] == "availability") ||
            (parts.size == 3 && parts.take(2) == listOf("api", "menu-items"))) {
            method(x, "PATCH")
            val b = body(x)
            val isStore = parts[1] == "stores"
            val field = if (isStore) "isOpen" else "available"
            b.fieldsExactly(field); val enabled = b.bool(field)
            return 200 to db.write { c ->
                val s = session(c, x); role(s, "merchant")
                val target = if (isStore) store(c, parts[2]) else
                    c.all("stores").firstOrNull { it["menu"].any { m -> m["id"].asText() == parts[2] } } as ObjectNode?
                        ?: fail(404, "not_found", "Menu item not found")
                if (actor(c, s)["storeId"] != target["id"]) fail(403, "forbidden", "You do not own this store")
                if (isStore) target.put(field, enabled) else (target["menu"].first { it["id"].asText() == parts[2] } as ObjectNode).put(field, enabled)
                c.save("stores", target["id"].asText(), target)
                target
            }
        }
        if (path == "/api/orders") {
            if (x.requestMethod == "GET") return 200 to db.read { c ->
                val s = session(c, x)
                node(c.all("orders").filter { visible(it, s) }.reversed())
            }
            method(x, "POST")
            val b = body(x)
            return db.write { c -> checkout(c, session(c, x), b) }
        }
        if (parts.size in 3..4 && parts.take(2) == listOf("api", "orders")) {
            if (parts.size == 3) {
                method(x, "GET")
                return 200 to db.read { c -> order(c, parts[2], session(c, x)) }
            }
            requireInput(parts[3].length <= 50, "Invalid action")
            if (parts[3] !in setOf("status", "claim", "location")) fail(404, "not_found", "Route not found")
            method(x, "POST")
            val b = body(x)
            return 200 to db.write { c -> changeOrder(c, session(c, x), parts[2], parts[3], b) }
        }
        fail(404, "not_found", "Route not found")
    }

    private fun checkout(c: Connection, s: JsonNode, b: JsonNode): Pair<Int, JsonNode> {
        role(s, "customer")
        b.fieldsExactly("storeId", "items", "deliveryAddress", "paymentMethod", "idempotencyKey")
        val storeId = b.text("storeId")
        val address = b.text("deliveryAddress", 8, 300, trim = true)
        val paymentMethod = b.text("paymentMethod")
        requireInput(paymentMethod in setOf("demo_card", "demo_decline"), "Unsupported simulated payment method")
        val key = b.text("idempotencyKey", 8, 100)
        val requested = b["items"]
        requireInput(requested.isArray && requested.size() in 1..30, "Choose 1..30 distinct menu items")
        val items = requested.map {
            it.fieldsExactly("menuItemId", "quantity")
            val menuId = it.text("menuItemId")
            val q = it["quantity"]
            requireInput(q.isIntegralNumber && q.canConvertToInt() && q.asInt() in 1..20, "Quantity must be an integer from 1 to 20")
            menuId to q.asInt()
        }
        requireInput(items.map { it.first }.distinct().size == items.size, "Duplicate menu items are not allowed")
        val payload = obj("storeId" to storeId, "items" to items.map { obj("menuItemId" to it.first, "quantity" to it.second) },
            "deliveryAddress" to address, "paymentMethod" to paymentMethod).toString()
        c.prepareStatement("SELECT payload,status,body FROM idempotency WHERE actor=? AND key=?").use { st ->
            st.setString(1, s["actorId"].asText()); st.setString(2, key)
            st.executeQuery().use { r ->
                if (r.next()) {
                    if (r.getString(1) != payload) fail(409, "idempotency_conflict", "This idempotency key was used with a different payload")
                    return (if (r.getInt(2) == 201) 200 else r.getInt(2)) to json.readTree(r.getString(3))
                }
            }
        }
        val store = store(c, storeId)
        if (!store["isOpen"].asBoolean()) fail(409, "store_closed", "Store is closed")
        val lines = items.map { (itemId, quantity) ->
            val m = store["menu"].firstOrNull { it["id"].asText() == itemId }
                ?: fail(400, "invalid_item", "Menu item does not belong to this store")
            if (!m["available"].asBoolean()) fail(409, "item_unavailable", "A selected menu item is unavailable")
            obj("menuItemId" to itemId, "name" to m["name"].asText(), "quantity" to quantity, "unitPriceCents" to m["priceCents"].asInt())
        }
        val subtotal = lines.sumOf { it["quantity"].asLong() * it["unitPriceCents"].asLong() }
        if (subtotal < store["minimumCents"].asLong()) fail(409, "minimum_not_met", "Store minimum has not been met")
        val tax = (subtotal + 5) / 10
        val delivery = store["deliveryFeeCents"].asLong()
        val result: Pair<Int, JsonNode>
        if (paymentMethod == "demo_decline") {
            result = 402 to obj("error" to obj("code" to "payment_declined", "message" to "Simulated payment was declined"))
        } else {
            val at = now()
            val o = obj("id" to id(), "storeId" to storeId, "storeName" to store["name"].asText(),
                "customerId" to s["actorId"].asText(), "customerName" to actor(c, s)["name"].asText(), "deliveryAddress" to address,
                "items" to lines, "totals" to obj("subtotalCents" to subtotal, "taxCents" to tax, "deliveryFeeCents" to delivery, "serviceFeeCents" to 149, "totalCents" to subtotal + tax + delivery + 149),
                "status" to "pending", "payment" to obj("id" to id(), "status" to "captured", "method" to "simulated"),
                "courierId" to null, "courierName" to null, "location" to null, "createdAt" to at, "updatedAt" to at,
                "events" to listOf(obj("status" to "pending", "at" to at, "note" to "Order placed; simulated payment captured")))
            c.save("orders", o["id"].asText(), o)
            result = 201 to o
        }
        c.prepareStatement("INSERT INTO idempotency(actor,key,payload,status,body) VALUES (?,?,?,?,?)").use {
            it.setString(1, s["actorId"].asText()); it.setString(2, key); it.setString(3, payload)
            it.setInt(4, result.first); it.setString(5, result.second.toString()); it.executeUpdate()
        }
        return result
    }

    private fun changeOrder(c: Connection, s: JsonNode, key: String, action: String, b: JsonNode): ObjectNode {
        // A losing claimant gets a conflict, without receiving another courier's order.
        if (action == "claim") {
            role(s, "courier"); b.fieldsExactly()
            val existing = c.find("orders", key) ?: fail(404, "not_found", "Order not found")
            if (existing["status"].asText() != "ready" || !existing["courierId"].isNull)
                fail(409, "claim_conflict", "Order is not available to claim")
            existing.put("courierId", s["actorId"].asText())
            existing.put("courierName", actor(c, s)["name"].asText())
            val at = now()
            existing.put("updatedAt", at)
            existing.withArray("events").add(obj("status" to "ready", "at" to at, "note" to "Courier claimed order"))
            c.save("orders", key, existing)
            return existing
        }
        val o = order(c, key, s)
        val current = o["status"].asText()
        val at = now()
        if (action == "location") {
            role(s, "courier"); b.fieldsExactly("lat", "lng")
            if (o["courierId"] != s["actorId"]) fail(403, "forbidden", "Claim the order before updating its location")
            if (current !in setOf("ready", "picked_up")) fail(409, "invalid_transition", "Location updates require an active delivery")
            val lat = b["lat"]; val lng = b["lng"]
            requireInput(lat.isNumber && lng.isNumber && lat.asDouble().isFinite() && lng.asDouble().isFinite() &&
                lat.asDouble() in -90.0..90.0 && lng.asDouble() in -180.0..180.0, "Coordinates must be finite and within Earth bounds")
            o.set<JsonNode>("location", obj("lat" to lat.asDouble(), "lng" to lng.asDouble(), "updatedAt" to at))
        } else {
            b.fieldsExactly("status")
            val target = b.text("status")
            requireInput(target in setOf("pending", "accepted", "preparing", "ready", "picked_up", "delivered", "cancelled"), "Unknown status")
            val allowed = when (s["role"].asText()) {
                "customer" -> current == "pending" && target == "cancelled"
                "merchant" -> when (current) {
                    "pending" -> target in setOf("accepted", "cancelled")
                    "accepted" -> target in setOf("preparing", "cancelled")
                    "preparing" -> target in setOf("ready", "cancelled")
                    "ready" -> target == "cancelled"
                    else -> false
                }
                "courier" -> {
                    if (o["courierId"] != s["actorId"]) fail(403, "forbidden", "Claim the order before changing its status")
                    (current == "ready" && target == "picked_up") || (current == "picked_up" && target == "delivered")
                }
                else -> false
            }
            if (!allowed) fail(409, "invalid_transition", "This role cannot perform the requested transition")
            o.put("status", target)
            if (target == "cancelled") (o["payment"] as ObjectNode).put("status", "refunded")
            o.withArray("events").add(obj("status" to target, "at" to at, "note" to "Status updated by ${s["role"].asText()}"))
        }
        o.put("updatedAt", at)
        c.save("orders", key, o)
        return o
    }
}

fun main() {
    val db = Database(Path.of(System.getenv("COOKIST_DB") ?: ".data/cookist.db"), Path.of(System.getenv("COOKIST_CATALOG") ?: "shared/catalog.json"))
    val server = CoreServer(db, System.getenv("HOST") ?: "127.0.0.1", (System.getenv("PORT") ?: "8081").toInt())
    Runtime.getRuntime().addShutdownHook(Thread { server.close() })
    server.start()
    println("Cookist core listening on port ${server.port} (local demo only)")
}
