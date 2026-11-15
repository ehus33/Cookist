import XCTest
@testable import Cookist

final class CookistTests: XCTestCase {
    func testNullableInitialLocationDecodes() throws {
        let data = Data("""
        {"id":"o1","storeId":"hearth","storeName":"Hearth","customerName":"Alex","deliveryAddress":"123 Main Street","status":"pending","courierId":null,"courierName":null,"location":null,"items":[],"totals":{"subtotalCents":1695,"taxCents":170,"deliveryFeeCents":199,"serviceFeeCents":149,"totalCents":2213},"payment":{"status":"captured","method":"simulated"},"events":[]}
        """.utf8)
        let order = try JSONDecoder().decode(Order.self, from: data)
        XCTAssertNil(order.location)
        XCTAssertNil(order.courierId)
        XCTAssertEqual(order.totals.totalCents, 2213)
    }
    func testOnlyAssignedCourierCanAdvance() {
        XCTAssertEqual(transitions(role: "courier", status: "ready", assigned: false), [])
        XCTAssertEqual(transitions(role: "courier", status: "ready", assigned: true), ["picked_up"])
        XCTAssertEqual(transitions(role: "courier", status: "picked_up", assigned: true), ["delivered"])
        XCTAssertEqual(transitions(role: "customer", status: "accepted", assigned: false), [])
    }
}
