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
import java.security.KeyPair
import java.security.KeyPairGenerator
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNotEquals
import kotlin.test.assertFalse

class CognitoAuthTest {
    private val issuer = "https://cognito-idp.us-west-2.amazonaws.com/us-west-2_test"
    private val keys: KeyPair = KeyPairGenerator.getInstance("RSA").apply { initialize(2048) }.generateKeyPair()
    private val auth = CognitoAuth(issuer, setOf("client123"), VerificationKeyResolver { _, _ -> keys.public })
    private val http = HttpClient.newHttpClient()

    private fun token(
        subject: String = "user-one", tokenUse: String = "access", client: String = "client123",
        tokenIssuer: String = issuer, lifetime: Float = 5f, signingKeys: KeyPair = keys,
        groups: List<String> = emptyList(), audience: String? = null
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

    private fun request(server: CoreServer, method: String, path: String, authorization: String? = null): HttpResponse<String> {
        val builder = HttpRequest.newBuilder(URI("http://127.0.0.1:${server.port}$path"))
        if (authorization != null) builder.header("Authorization", authorization)
        return http.send(builder.method(method, HttpRequest.BodyPublishers.noBody()).build(), HttpResponse.BodyHandlers.ofString())
    }

    @Test
    fun `configuration must not silently enable demo with partial Cognito settings`() {
        assertEquals("demo", authMode(emptyMap()))
        assertFailsWith<IllegalArgumentException> { authMode(mapOf("COOKIST_COGNITO_ISSUER" to issuer)) }
        assertFailsWith<IllegalArgumentException> { authMode(mapOf("COOKIST_COGNITO_CLIENT_IDS" to "client123")) }
        assertFailsWith<IllegalArgumentException> { CognitoAuth.create(issuer, emptySet()) }
        assertFailsWith<IllegalArgumentException> { CognitoAuth.create("http://example.com/pool", setOf("client123")) }
    }

    @Test
    fun `only valid access tokens from the configured pool and client are accepted`() {
        val expected = CognitoIdentity("${auth.actorPrefix}user-one")
        assertEquals(expected, auth.authenticate(token()))
        assertEquals(expected, auth.authenticate(token(groups = listOf("group-a", "group-b"))))
        val otherAuth = CognitoAuth("$issuer-other", setOf("client123"), VerificationKeyResolver { _, _ -> keys.public })
        assertNotEquals(auth.actorPrefix, otherAuth.actorPrefix)
        val wrongKey = KeyPairGenerator.getInstance("RSA").apply { initialize(2048) }.generateKeyPair()
        val invalid = listOf(
            token(tokenUse = "id"), token(client = "other"), token(tokenIssuer = "$issuer-other"),
            token(lifetime = -5f), token(signingKeys = wrongKey), token(subject = ""),
            token(audience = "different-api"), "not-a-token"
        )
        invalid.forEach { assertEquals(401, assertFailsWith<ApiError> { auth.authenticate(it) }.status) }
    }

    @Test
    fun `me exposes only stable identity and rejects invalid authentication`() {
        CoreServer(auth, port = 0).use { server ->
            server.start()
            val response = request(server, "GET", "/api/me", "Bearer ${token()}")
            assertEquals(200, response.statusCode())
            assertEquals(json.readTree("""{"actorId":"${auth.actorPrefix}user-one"}"""), json.readTree(response.body()))
            assertEquals("no-store", response.headers().firstValue("Cache-Control").orElse(""))
            for (header in listOf(null, "Basic abc", "Bearer not-a-token", "Bearer ${token(tokenUse = "id")}")) {
                assertEquals(401, request(server, "GET", "/api/me", header).statusCode())
            }
            assertEquals(405, request(server, "POST", "/api/me").statusCode())
        }
    }

    @Test
    fun `unexpected authentication failures log only safe diagnostics`() {
        val secret = "sensitive-diagnostic-content"
        val messages = mutableListOf<String>()
        CoreServer(auth, port = 0, logError = { messages.add(it) },
            verifyToken = { _, _ -> throw IllegalStateException(secret) }).use { server ->
            server.start()
            val accessToken = token()
            val response = request(server, "GET", "/api/me", "Bearer $accessToken")
            assertEquals(500, response.statusCode())
            assertEquals("internal_error", json.readTree(response.body())["error"]["code"].asText())
            assertEquals(listOf("Core request failed unexpectedly (java.lang.IllegalStateException)"), messages)
            assertFalse(messages.single().contains(secret))
            assertFalse(messages.single().contains(accessToken))
        }
    }

    @Test
    fun `health works without auth and removed routes do not exist in either mode`() {
        for (cognito in listOf(null, auth)) {
            CoreServer(cognito, port = 0).use { server ->
                server.start()
                val health = request(server, "GET", "/health")
                assertEquals(200, health.statusCode())
                assertEquals("ok", json.readTree(health.body())["status"].asText())
                if (cognito == null) {
                    assertEquals(401, request(server, "GET", "/api/me").statusCode())
                    assertEquals(401, request(server, "GET", "/api/me", "Bearer ${token()}").statusCode())
                }
                for ((method, path) in listOf(
                    "GET" to "/api/stores", "GET" to "/api/stores/example",
                    "GET" to "/api/orders", "POST" to "/api/orders",
                    "POST" to "/api/orders/example/location", "POST" to "/api/orders/example/claim",
                    "GET" to "/api/demo-accounts", "POST" to "/api/sessions"
                )) {
                    assertEquals(404, request(server, method, path, "Bearer ${token()}").statusCode(), path)
                }
            }
        }
    }
}
