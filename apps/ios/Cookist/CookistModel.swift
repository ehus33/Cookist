import Foundation
import SwiftUI

@MainActor final class CookistModel: ObservableObject {
    @Published var baseURL = UserDefaults.standard.string(forKey: "baseURL") ?? "http://localhost:8080"
    @Published var session: Session?
    @Published var stores: [Store] = []
    @Published var orders: [Order] = []
    @Published var accounts = Accounts(customers: [], merchants: [], couriers: [])
    @Published var error: String?
    @Published var busy = false
    @Published var cart: [String: Int] = [:]
    @Published var cartStore: Store?
    @Published private(set) var checkoutPending = false
    @Published var checkoutAddress = ""
    @Published var checkoutDecline = false
    private var checkoutKey = UUID().uuidString
    private var checkoutPayload = Data()
    var role: String { session?.role ?? "guest" }

    init() {
        let defaults = UserDefaults.standard
        if let data = defaults.data(forKey: "session") { session = try? JSONDecoder().decode(Session.self, from: data) }
        if let data = defaults.data(forKey: "cart") { cart = (try? JSONDecoder().decode([String: Int].self, from: data)) ?? [:] }
        if let data = defaults.data(forKey: "cartStore") { cartStore = try? JSONDecoder().decode(Store.self, from: data) }
    }
    private func request<T: Decodable>(_ path: String, method: String = "GET", body: [String: Any]? = nil) async throws -> T {
        guard let url = URL(string: baseURL + path) else { throw URLError(.badURL) }
        var request = URLRequest(url: url, timeoutInterval: 12)
        request.httpMethod = method
        if let token = session?.token { request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        if let body {
            request.httpBody = try JSONSerialization.data(withJSONObject: body, options: [.sortedKeys])
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let response = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard (200...299).contains(response.statusCode) else {
            let detail = (try? JSONDecoder().decode(APIError.self, from: data))?.error.message
            throw NSError(domain: "Cookist", code: response.statusCode, userInfo: [NSLocalizedDescriptionKey: "HTTP \(response.statusCode): \(detail ?? String(data: data, encoding: .utf8) ?? "Request failed")"])
        }
        return try JSONDecoder().decode(T.self, from: data)
    }
    func action(_ work: @escaping @MainActor () async throws -> Void) {
        guard !busy else { return }
        busy = true
        Task {
            defer { busy = false }
            do { try await work() }
            catch is CancellationError {}
            catch { self.error = error.localizedDescription }
        }
    }
    func refresh() async throws {
        let freshStores: [Store] = try await request("/api/stores")
        let freshOrders: [Order]
        if session != nil { freshOrders = try await request("/api/orders") } else { freshOrders = [] }
        stores = freshStores
        orders = freshOrders
        if let id = cartStore?.id, let current = freshStores.first(where: { $0.id == id }) {
            cartStore = current
            persistCart()
        }
    }
    func load() {
        action {
            self.accounts = try await self.request("/api/demo-accounts")
            try await self.refresh()
        }
    }
    func search(_ query: String, sort: String, open: Bool) {
        action {
            var components = URLComponents()
            components.queryItems = [URLQueryItem(name: "q", value: String(query.prefix(100))), URLQueryItem(name: "sort", value: sort), URLQueryItem(name: "openOnly", value: String(open))]
            let result: SearchResult = try await self.request("/api/search?" + (components.percentEncodedQuery ?? ""))
            self.stores = result.stores
        }
    }
    func login(_ role: String, actor: String) {
        action {
            let session: Session = try await self.request("/api/sessions", method: "POST", body: ["role": role, "actorId": actor])
            self.session = session
            self.checkoutPayload = Data()
            UserDefaults.standard.set(try JSONEncoder().encode(session), forKey: "session")
            self.orders = []
            try await self.refresh()
        }
    }
    func saveServer(_ value: String) {
        guard !busy else { return }
        let value = value.trimmingCharacters(in: .whitespacesAndNewlines).trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        guard let url = URLComponents(string: value), ["http", "https"].contains(url.scheme ?? ""), url.host != nil, url.user == nil, url.password == nil, url.query == nil, url.fragment == nil, url.path.isEmpty else {
            error = "Enter an http(s) server origin without a path."; return
        }
        baseURL = value
        session = nil
        checkoutPayload = Data()
        orders = []
        UserDefaults.standard.set(value, forKey: "baseURL")
        UserDefaults.standard.removeObject(forKey: "session")
        load()
    }
    func quantity(_ store: Store, _ item: MenuItem, delta: Int) {
        guard !busy else { return }
        guard cart.isEmpty || cartStore?.id == store.id else {
            error = "Your basket is from another kitchen. Clear it first."; return
        }
        let next = min(20, max(0, (cart[item.id] ?? 0) + delta))
        cart[item.id] = next == 0 ? nil : next
        cartStore = store
        persistCart()
    }
    private func persistCart() {
        UserDefaults.standard.set(try? JSONEncoder().encode(cart), forKey: "cart")
        UserDefaults.standard.set(try? JSONEncoder().encode(cartStore), forKey: "cartStore")
    }
    func clearCart() {
        guard !busy else { return }
        resetCart()
    }
    private func resetCart() {
        cart = [:]
        cartStore = nil
        persistCart()
    }
    func checkout(address: String, decline: Bool) {
        guard !busy, let store = cartStore else { return }
        let items = cart.keys.sorted().map { ["menuItemId": $0, "quantity": cart[$0]!] as [String: Any] }
        let snapshot: [String: Any] = [
            "storeId": store.id,
            "items": items,
            "deliveryAddress": address.trimmingCharacters(in: .whitespacesAndNewlines),
            "paymentMethod": decline ? "demo_decline" : "demo_card"
        ]
        checkoutPending = true
        action {
            defer { self.checkoutPending = false }
            var body = snapshot
            let payload = try JSONSerialization.data(withJSONObject: body, options: [.sortedKeys])
            if payload != self.checkoutPayload {
                self.checkoutKey = UUID().uuidString
                self.checkoutPayload = payload
            }
            body["idempotencyKey"] = self.checkoutKey
            let _: Order = try await self.request("/api/orders", method: "POST", body: body)
            self.resetCart()
            self.checkoutPayload = Data()
            try await self.refresh()
        }
    }
    func changeStore(_ store: Store) {
        action {
            let _: Store = try await self.request("/api/stores/\(store.id)/availability", method: "PATCH", body: ["isOpen": !store.isOpen])
            try await self.refresh()
        }
    }
    func changeItem(_ item: MenuItem) {
        action {
            let _: Store = try await self.request("/api/menu-items/\(item.id)", method: "PATCH", body: ["available": !item.available])
            try await self.refresh()
        }
    }
    func mutateOrder(_ id: String, suffix: String, body: [String: Any]) {
        action {
            let _: Order = try await self.request("/api/orders/\(id)/\(suffix)", method: "POST", body: body)
            try await self.refresh()
        }
    }
}
