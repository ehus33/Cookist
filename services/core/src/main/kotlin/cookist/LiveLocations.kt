package cookist

import java.time.Instant
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

internal data class LivePosition(val lat: Double, val lng: Double, val updatedAt: String, val source: String = "live")

internal class LiveLocations(
    private val nanoTime: () -> Long = System::nanoTime,
    private val now: () -> Instant = Instant::now
) : AutoCloseable {
    private data class Sharing(val courier: String, val id: String, var lastActivity: Long, var position: LivePosition?)
    private val sessions = mutableMapOf<String, Sharing>()
    private val expiry = TimeUnit.SECONDS.toNanos(60)
    private val cleaner = Executors.newSingleThreadScheduledExecutor().apply {
        scheduleAtFixedRate({ purgeExpired() }, 30, 30, TimeUnit.SECONDS)
    }

    @Synchronized private fun purgeExpired() {
        val current = nanoTime()
        sessions.entries.removeIf { current - it.value.lastActivity >= expiry }
    }

    @Synchronized fun start(orderId: String, courier: String): String {
        purgeExpired()
        val id = UUID.randomUUID().toString()
        sessions[orderId] = Sharing(courier, id, nanoTime(), null)
        return id
    }

    @Synchronized fun update(orderId: String, courier: String, id: String, lat: Double, lng: Double): Boolean {
        purgeExpired()
        val sharing = sessions[orderId] ?: return false
        if (sharing.courier != courier || sharing.id != id) return false
        sharing.lastActivity = nanoTime()
        sharing.position = LivePosition(lat, lng, now().toString())
        return true
    }

    @Synchronized fun stop(orderId: String, courier: String, id: String): Boolean {
        purgeExpired()
        val sharing = sessions[orderId] ?: return false
        if (sharing.courier != courier || sharing.id != id) return false
        sessions.remove(orderId)
        return true
    }

    @Synchronized fun clear(orderId: String) { sessions.remove(orderId) }

    @Synchronized fun get(orderId: String, courier: String): LivePosition? {
        purgeExpired()
        return sessions[orderId]?.takeIf { it.courier == courier }?.position
    }

    override fun close() {
        cleaner.shutdownNow()
        synchronized(this) { sessions.clear() }
    }
}
