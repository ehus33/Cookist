package cookist

import org.jose4j.jws.AlgorithmIdentifiers
import org.jose4j.jws.JsonWebSignature
import org.jose4j.jwt.JwtClaims
import org.jose4j.keys.resolvers.VerificationKeyResolver
import org.junit.jupiter.api.Test
import java.net.URI
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import java.nio.file.Files
import java.nio.file.Path
import java.security.KeyPair
import java.security.KeyPairGenerator
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertTrue

class CognitoAuthTest {
    private val issuer = "https://cognito-idp.us-west-2.amazonaws.com/us-west-2_test"
    private val keys: KeyPair = KeyPairGenerator.getInstance("RSA").apply { initialize(2048) }.generateKeyPair()
    private val auth = CognitoAuth(issuer, setOf("client123"), VerificationKeyResolver { _, _ -> keys.public })

    @Test
    fun `partial Cognito configuration does not silently enable demo authentication`() {
        assertEquals("demo", authMode(emptyMap()))
        assertFailsWith<IllegalArgumentException> { authMode(mapOf("COOKIST_COGNITO_ISSUER" to issuer)) }
        assertFailsWith<IllegalArgumentException> { authMode(mapOf("COOKIST_COGNITO_CLIENT_IDS" to "client123")) }
        assertFailsWith<IllegalArgumentException> { CognitoAuth.create(issuer, emptySet()) }
        assertFailsWith<IllegalArgumentException> { CognitoAuth.create("http://example.com/pool", setOf("client123")) }
    }

    private fun token(
        subject: String = "user-one", tokenUse: String = "access", client: String = "client123",
        tokenIssuer: String = issuer, lifetime: Float = 5f, signingKeys: KeyPair = keys, groups: List<String> = emptyList(),
        audience: String? = null
    ): String {
        val claims = JwtClaims().apply {
            setIssuer(tokenIssuer); setSubject(subject); setExpirationTimeMinutesInTheFuture(lifetime)
            setStringClaim("token_use", tokenUse); setStringClaim("client_id", client)
            if (groups.isNotEmpty()) setStringListClaim("cognito:groups", groups)
            if (audience != null) setAudience(audience)
        }
        return JsonWebSignature().apply {
            payload = claims.toJson(); key = signingKeys.private
            algorithmHeaderValue = AlgorithmIdentifiers.RSA_USING_SHA256
        }.compactSerialization
    }

    @Test
    fun `only valid access tokens from the configured pool and client are accepted`() {
        assertEquals(CognitoIdentity("${auth.actorPrefix}user-one", "customer"), auth.authenticate(token()))
        assertEquals(CognitoIdentity("${auth.actorPrefix}user-one", "courier"), auth.authenticate(token(groups = listOf("cookist-courier"))))
        assertEquals(CognitoIdentity("${auth.actorPrefix}user-one", "merchant", "hearth"), auth.authenticate(token(groups = listOf("cookist-merchant-hearth"))))
        assertEquals(403, assertFailsWith<ApiError> { auth.authenticate(token(groups = listOf("cookist-courier", "cookist-merchant-hearth"))) }.status)
        val wrongKey = KeyPairGenerator.getInstance("RSA").apply { initialize(2048) }.generateKeyPair()
        val invalid = listOf(
            token(tokenUse = "id"), token(client = "other"), token(tokenIssuer = "$issuer-other"),
            token(lifetime = -5f), token(signingKeys = wrongKey), token(subject = ""), token(audience = "different-api"),
            "not-a-token"
        )
        invalid.forEach { assertEquals(401, assertFailsWith<ApiError> { auth.authenticate(it) }.status) }
    }

    @Test
    fun `demo database cannot be reused for Cognito`() {
        val directory = Files.createTempDirectory("cookist-auth-mode")
        try {
            val db = Database(directory.resolve("core.db"), Path.of("shared", "catalog.json"))
            CoreServer(db, port = 0).use { it.start() }
            assertFailsWith<IllegalArgumentException> { CoreServer(db, auth, port = 0) }
        } finally {
            Files.walk(directory).use { it.sorted(Comparator.reverseOrder()).forEach(Files::deleteIfExists) }
        }
    }

    @Test
    fun `cognito mode disables demo impersonation and isolates customer orders`() {
        val directory = Files.createTempDirectory("cookist-auth-test")
        try {
            val db = Database(directory.resolve("core.db"), Path.of("shared", "catalog.json"))
            CoreServer(db, auth, port = 0).use { server ->
                server.start()
                val http = HttpClient.newHttpClient()
                fun request(method: String, path: String, bearer: String? = null, body: String? = null): HttpResponse<String> {
                    val builder = HttpRequest.newBuilder(URI("http://127.0.0.1:${server.port}$path"))
                        .header("Content-Type", "application/json")
                    if (bearer != null) builder.header("Authorization", "Bearer $bearer")
                    return http.send(builder.method(method, body?.let { HttpRequest.BodyPublishers.ofString(it) }
                        ?: HttpRequest.BodyPublishers.noBody()).build(), HttpResponse.BodyHandlers.ofString())
                }
                assertEquals(404, request("GET", "/api/demo-accounts").statusCode())
                assertEquals(404, request("POST", "/api/sessions", body = """{"role":"merchant","actorId":"merchant-hearth"}""").statusCode())
                assertEquals(401, request("GET", "/api/orders", "not-a-session").statusCode())
                val first = token(subject = "customer-1")
                val second = token(subject = "customer-2")
                val body = """{"storeId":"hearth","items":[{"menuItemId":"hearth-margherita","quantity":1}],"deliveryAddress":"123 Local Street","paymentMethod":"demo_card","idempotencyKey":"test-key-123"}"""
                val placed = request("POST", "/api/orders", first, body)
                assertEquals(201, placed.statusCode(), placed.body())
                val order = json.readTree(placed.body())
                assertEquals("${auth.actorPrefix}customer-1", order["customerId"].asText())
                assertEquals(404, request("GET", "/api/orders/${order["id"].asText()}", second).statusCode())
                assertEquals(403, request("PATCH", "/api/stores/hearth/availability", first, """{"isOpen":false}""").statusCode())
                assertTrue(json.readTree(request("GET", "/api/orders", second).body()).isEmpty)
                val id = order["id"].asText()
                val merchant = token(subject = "merchant", groups = listOf("cookist-merchant-hearth"))
                val courier = token(subject = "courier", groups = listOf("cookist-courier"))
                val otherCourier = token(subject = "other", groups = listOf("cookist-courier"))
                val otherMerchant = token(subject = "other", groups = listOf("cookist-merchant-green"))
                assertEquals(404, request("GET", "/api/orders/$id", otherMerchant).statusCode())
                for (status in listOf("accepted", "preparing", "ready")) {
                    assertEquals(200, request("POST", "/api/orders/$id/status", merchant, """{"status":"$status"}""").statusCode())
                }
                assertFailsWith<IllegalArgumentException> { CoreServer(db, port = 0) }
                val otherIssuer = "https://cognito-idp.us-west-2.amazonaws.com/us-west-2_other"
                val otherAuth = CognitoAuth(otherIssuer, setOf("client123"), VerificationKeyResolver { _, _ -> keys.public })
                assertFailsWith<IllegalArgumentException> { CoreServer(db, otherAuth, port = 0) }
                assertEquals(200, request("POST", "/api/orders/$id/claim", courier, "{}").statusCode())
                assertEquals(404, request("GET", "/api/orders/$id", otherCourier).statusCode())
                assertEquals(403, request("POST", "/api/orders/$id/location-start", first, "{}").statusCode())
                assertEquals(404, request("POST", "/api/orders/$id/location-start", otherCourier, "{}").statusCode())
                assertEquals(409, request("POST", "/api/orders/$id/location", courier, """{"lat":47.6,"lng":-122.3,"sharingId":"old"}""").statusCode())
                val started = request("POST", "/api/orders/$id/location-start", courier, "{}")
                assertEquals(200, started.statusCode())
                val sharingId = json.readTree(started.body())["sharingId"].asText()
                assertEquals(200, request("POST", "/api/orders/$id/location", courier, """{"lat":47.6,"lng":-122.3,"sharingId":"$sharingId"}""").statusCode())
                assertEquals(47.6, json.readTree(request("GET", "/api/orders/$id", first).body())["location"]["lat"].asDouble())
                assertTrue(db.read { c ->
                    c.prepareStatement("SELECT body FROM orders WHERE id=?").use { statement ->
                        statement.setString(1, id)
                        statement.executeQuery().use { result -> result.next(); json.readTree(result.getString(1))["location"].isNull }
                    }
                })
                assertEquals(200, request("POST", "/api/orders/$id/location-stop", courier, """{"sharingId":"$sharingId"}""").statusCode())
                assertTrue(json.readTree(request("GET", "/api/orders/$id", first).body())["location"].isNull)
                assertEquals(409, request("POST", "/api/orders/$id/location", courier, """{"lat":47.6,"lng":-122.3,"sharingId":"$sharingId"}""").statusCode())
                val secondSharing = json.readTree(request("POST", "/api/orders/$id/location-start", courier, "{}").body())["sharingId"].asText()
                assertEquals(200, request("POST", "/api/orders/$id/location", courier, """{"lat":47.6,"lng":-122.3,"sharingId":"$secondSharing"}""").statusCode())
                for (status in listOf("picked_up", "delivered")) {
                    assertEquals(200, request("POST", "/api/orders/$id/status", courier, """{"status":"$status"}""").statusCode())
                }
                assertTrue(json.readTree(request("GET", "/api/orders/$id", first).body())["location"].isNull)
            }
        } finally {
            Files.walk(directory).use { it.sorted(Comparator.reverseOrder()).forEach(Files::deleteIfExists) }
        }
    }
}
