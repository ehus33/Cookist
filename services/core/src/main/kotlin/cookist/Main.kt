package cookist

import com.fasterxml.jackson.databind.ObjectMapper
import com.sun.net.httpserver.HttpExchange
import com.sun.net.httpserver.HttpServer
import java.net.InetSocketAddress
import java.util.concurrent.Executors

internal val json = ObjectMapper()
internal class ApiError(val status: Int, val code: String, override val message: String) : RuntimeException(message)

internal class CoreServer(
    private val cognito: CognitoAuth? = null,
    host: String = "127.0.0.1",
    port: Int = 8081,
    private val logError: (String) -> Unit = { System.err.println(it) },
    private val verifyToken: (CognitoAuth, String) -> CognitoIdentity = { auth, token -> auth.authenticate(token) }
) : AutoCloseable {
    private val executor = Executors.newFixedThreadPool(16)
    private val server = HttpServer.create(InetSocketAddress(host, port), 64).apply {
        executor = this@CoreServer.executor
        createContext("/") { exchange -> handle(exchange) }
    }

    val port: Int get() = server.address.port
    fun start() = server.start()
    override fun close() { server.stop(0); executor.shutdownNow() }

    private fun handle(exchange: HttpExchange) {
        try {
            val path = exchange.requestURI.rawPath
            if (path != "/health" && path != "/api/me") throw ApiError(404, "not_found", "Route not found")
            if (exchange.requestMethod != "GET") {
                exchange.responseHeaders.set("Allow", "GET")
                throw ApiError(405, "method_not_allowed", "Use GET")
            }
            val response = if (path == "/health") {
                mapOf("status" to "ok")
            } else {
                val auth = cognito ?: throw ApiError(401, "unauthorized", "Authentication is unavailable")
                val header = exchange.requestHeaders.getFirst("Authorization")
                if (header == null || !header.startsWith("Bearer ") || header.length > 16384) {
                    throw ApiError(401, "unauthorized", "Invalid access token")
                }
                mapOf("actorId" to verifyToken(auth, header.removePrefix("Bearer ")).subject)
            }
            respond(exchange, 200, response)
        } catch (e: ApiError) {
            respond(exchange, e.status, mapOf("error" to mapOf("code" to e.code, "message" to e.message)))
        } catch (e: Exception) {
            logError("Core request failed unexpectedly (${e.javaClass.name})")
            respond(exchange, 500, mapOf("error" to mapOf("code" to "internal_error", "message" to "An internal error occurred")))
        } finally {
            exchange.close()
        }
    }

    private fun respond(exchange: HttpExchange, status: Int, value: Any) {
        val bytes = json.writeValueAsBytes(value)
        exchange.responseHeaders.set("Content-Type", "application/json; charset=utf-8")
        exchange.responseHeaders.set("Cache-Control", "no-store")
        exchange.responseHeaders.set("X-Content-Type-Options", "nosniff")
        exchange.sendResponseHeaders(status, bytes.size.toLong())
        exchange.responseBody.write(bytes)
    }
}

internal fun authMode(env: Map<String, String>): String {
    val mode = env["COOKIST_AUTH_MODE"]
    require(mode != null || (env["COOKIST_COGNITO_ISSUER"] == null && env["COOKIST_COGNITO_CLIENT_IDS"] == null)) {
        "COOKIST_AUTH_MODE must be explicit when Cognito settings are supplied"
    }
    return mode ?: "demo"
}

fun main() {
    val env = System.getenv()
    val mode = authMode(env)
    val cognito = when (mode) {
        "demo" -> null
        "cognito" -> {
            val issuer = requireNotNull(env["COOKIST_COGNITO_ISSUER"]) { "COOKIST_COGNITO_ISSUER is required" }
            val clients = requireNotNull(env["COOKIST_COGNITO_CLIENT_IDS"]) { "COOKIST_COGNITO_CLIENT_IDS is required" }
                .split(',').map { it.trim() }.toSet()
            CognitoAuth.create(issuer, clients)
        }
        else -> error("COOKIST_AUTH_MODE must be demo or cognito")
    }
    val server = CoreServer(cognito, env["HOST"] ?: "127.0.0.1", (env["PORT"] ?: "8081").toInt())
    Runtime.getRuntime().addShutdownHook(Thread { server.close() })
    server.start()
    println("Cookist core listening on port ${server.port} (auth mode: $mode)")
}
