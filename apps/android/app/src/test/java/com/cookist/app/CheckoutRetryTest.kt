package com.cookist.app

import org.junit.Assert.*
import org.junit.Test

class CheckoutRetryTest {
    @Test fun declinedRetryKeepsKeyButPaymentChangeCreatesNewKey() {
        val retry = CheckoutRetry()
        val cart = mapOf("hearth-margherita" to 1)
        val declined = retry.prepare("hearth", cart, "123 Main Street", true)
        assertEquals(declined, retry.prepare("hearth", cart, "123 Main Street", true))
        val approved = retry.prepare("hearth", cart, "123 Main Street", false)
        assertNotEquals(declined.key, approved.key)
        assertEquals("demo_card", approved.draft.paymentMethod)
        assertEquals(approved.key, retry.prepare("hearth", cart, "123 Main Street", false).key)
    }

    @Test fun snapshotSurvivesMutableCartEditsAndEquivalentReorderedRetry() {
        val retry = CheckoutRetry()
        val cart = linkedMapOf("b" to 2, "a" to 1)
        val attempt = retry.prepare("hearth", cart, "123 Main Street", false)
        cart["a"] = 20
        assertEquals(listOf(CheckoutLine("a", 1), CheckoutLine("b", 2)), attempt.draft.items)
        assertEquals(attempt.key, retry.prepare("hearth", linkedMapOf("a" to 1, "b" to 2), " 123 Main Street ", false).key)
        assertNotEquals(attempt.key, retry.prepare("hearth", cart, "123 Main Street", false).key)
        val changedAddress = retry.prepare("hearth", cart, "456 Main Street", false)
        assertEquals("456 Main Street", changedAddress.draft.deliveryAddress)
        retry.reset()
        assertNotEquals(changedAddress.key, retry.prepare("hearth", cart, "456 Main Street", false).key)
    }
}
