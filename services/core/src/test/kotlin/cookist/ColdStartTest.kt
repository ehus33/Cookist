package cookist

import org.junit.jupiter.api.Test
import java.net.URI
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import java.nio.file.Files
import java.nio.file.Path
import java.time.Duration
import java.util.UUID
import java.util.concurrent.TimeUnit
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import kotlin.test.fail

class ColdStartTest {
    @Test
    fun `announced readiness serves health and catalog within downstream deadlines in a cold JVM`() {
        val directory = Path.of("services", "core", "build", "test-data", UUID.randomUUID().toString()).toAbsolutePath()
        Files.createDirectories(directory)
        val catalog = Path.of("shared", "catalog.json").toAbsolutePath()
        val expected = json.readTree(catalog.toFile())
        val client = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(2)).build()
        try {
            // Restart against the same database, but never reuse a warmed JVM or mapper.
            for (attempt in 1..2) {
                val log = directory.resolve("startup-$attempt.log")
                val executable = if (System.getProperty("os.name").startsWith("Windows")) "java.exe" else "java"
                val process = ProcessBuilder(
                    Path.of(System.getProperty("java.home"), "bin", executable).toString(),
                    "-cp", System.getProperty("cookist.main.classpath"), "cookist.MainKt"
                ).apply {
                    environment()["HOST"] = "127.0.0.1"
                    environment()["PORT"] = "0"
                    environment()["COOKIST_DB"] = directory.resolve("cold.db").toString()
                    environment()["COOKIST_CATALOG"] = catalog.toString()
                    redirectErrorStream(true)
                    redirectOutput(log.toFile())
                }.start()
                try {
                    val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(60)
                    var port: Int? = null
                    while (System.nanoTime() < deadline) {
                        val output = Files.readString(log)
                        port = Regex("Cookist core listening on port (\\d+)").find(output)?.groupValues?.get(1)?.toInt()
                        if (port != null) break
                        assertTrue(process.isAlive, "Cold process exited before readiness: $output")
                        Thread.sleep(25)
                    }
                    if (port == null) fail("Cold process never announced readiness: ${Files.readString(log)}")
                    fun get(path: String): String {
                        val request = HttpRequest.newBuilder(URI("http://127.0.0.1:$port$path"))
                            .timeout(Duration.ofSeconds(2)).GET().build()
                        val started = System.nanoTime()
                        val response = client.send(request, HttpResponse.BodyHandlers.ofString())
                        val elapsed = TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - started)
                        println("Cold JVM $attempt $path completed in ${elapsed}ms")
                        assertEquals(200, response.statusCode(), response.body())
                        return response.body()
                    }
                    assertEquals("ok", json.readTree(get("/health"))["status"].asText())
                    assertEquals(expected, json.readTree(get("/api/stores")))
                } finally {
                    process.destroy()
                    if (!process.waitFor(5, TimeUnit.SECONDS)) {
                        process.destroyForcibly()
                        assertTrue(process.waitFor(5, TimeUnit.SECONDS), "Cold process did not stop")
                    }
                }
            }
        } finally {
            Files.walk(directory).use { paths -> paths.sorted(Comparator.reverseOrder()).forEach { Files.deleteIfExists(it) } }
        }
    }
}
