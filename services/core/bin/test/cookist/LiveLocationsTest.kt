package cookist

import org.junit.jupiter.api.Test
import java.time.Instant
import java.util.concurrent.TimeUnit
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

class LiveLocationsTest {
    @Test
    fun `sharing expires and stale sessions cannot update or stop a newer session`() {
        var clock = 0L
        LiveLocations({ clock }, { Instant.parse("2026-01-01T00:00:00Z") }).use { locations ->
            val first = locations.start("order", "courier")
            assertNull(locations.get("order", "courier"))
            assertFalse(locations.update("order", "other", first, 47.6, -122.3))
            assertTrue(locations.update("order", "courier", first, 47.6, -122.3))
            assertEquals(47.6, locations.get("order", "courier")?.lat)
            assertNull(locations.get("order", "other"))
            clock += TimeUnit.SECONDS.toNanos(60)
            assertNull(locations.get("order", "courier"))
            assertFalse(locations.update("order", "courier", first, 47.6, -122.3))
            val next = locations.start("order", "courier")
            assertFalse(locations.stop("order", "courier", first))
            assertTrue(locations.update("order", "courier", next, 47.7, -122.4))
            assertTrue(locations.stop("order", "courier", next))
            assertNull(locations.get("order", "courier"))
        }
    }
}
