import Foundation

struct Account: Codable, Identifiable {
    let id: String
    let name: String
    let storeId: String?
}
struct Accounts: Decodable {
    var customers: [Account]
    var merchants: [Account]
    var couriers: [Account]
}
struct Session: Codable {
    let token: String
    let role: String
    let actorId: String
}
struct MenuItem: Codable, Identifiable {
    let id: String
    let name: String
    let description: String
    let category: String
    let priceCents: Int
    let emoji: String
    let available: Bool
}
struct Store: Codable, Identifiable {
    let id: String
    let name: String
    let cuisine: String
    let description: String
    let rating: Double
    let etaMinutes: Int
    let deliveryFeeCents: Int
    let isOpen: Bool
    let emoji: String
    let menu: [MenuItem]
}
struct Order: Decodable, Identifiable {
    struct Item: Decodable {
        let menuItemId: String
        let name: String
        let quantity: Int
        let unitPriceCents: Int
    }
    struct Totals: Decodable {
        let subtotalCents: Int
        let taxCents: Int
        let deliveryFeeCents: Int
        let serviceFeeCents: Int
        let totalCents: Int
    }
    struct Payment: Decodable { let status: String; let method: String }
    struct Position: Decodable { let lat: Double; let lng: Double; let updatedAt: String }
    struct Event: Decodable { let status: String; let at: String; let note: String }
    let id: String
    let storeId: String
    let storeName: String
    let customerName: String
    let deliveryAddress: String
    let status: String
    let courierId: String?
    let courierName: String?
    let location: Position?
    let items: [Item]
    let totals: Totals
    let payment: Payment
    let events: [Event]
}
struct SearchResult: Decodable { let stores: [Store] }
struct APIError: Decodable {
    struct Detail: Decodable { let code: String; let message: String }
    let error: Detail
}
func money(_ cents: Int) -> String {
    String(format: "$%d.%02d", cents / 100, cents % 100)
}
func transitions(role: String, status: String, assigned: Bool) -> [String] {
    switch role {
    case "customer": return status == "pending" ? ["cancelled"] : []
    case "merchant":
        switch status {
        case "pending": return ["accepted", "cancelled"]
        case "accepted": return ["preparing", "cancelled"]
        case "preparing": return ["ready", "cancelled"]
        case "ready": return ["cancelled"]
        default: return []
        }
    case "courier":
        guard assigned else { return [] }
        return status == "ready" ? ["picked_up"] : status == "picked_up" ? ["delivered"] : []
    default: return []
    }
}
