package com.cookist.app

import java.util.UUID

internal data class CheckoutLine(val menuItemId: String, val quantity: Int)
internal data class CheckoutDraft(
    val storeId: String,
    val items: List<CheckoutLine>,
    val deliveryAddress: String,
    val paymentMethod: String
)
internal data class CheckoutAttempt(val draft: CheckoutDraft, val key: String)

internal class CheckoutRetry {
    private var previous: CheckoutAttempt? = null

    fun prepare(storeId: String, cart: Map<String, Int>, address: String, decline: Boolean): CheckoutAttempt {
        val draft = CheckoutDraft(
            storeId,
            cart.toSortedMap().map { CheckoutLine(it.key, it.value) },
            address.trim(),
            if (decline) "demo_decline" else "demo_card"
        )
        return previous?.takeIf { it.draft == draft }
            ?: CheckoutAttempt(draft, UUID.randomUUID().toString()).also { previous = it }
    }

    fun reset() { previous = null }
}
