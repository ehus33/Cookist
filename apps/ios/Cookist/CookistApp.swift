import SwiftUI

private let orange = Color(red: 0.84, green: 0.31, blue: 0.12)
private let cream = Color(red: 1, green: 0.97, blue: 0.94)

@main @MainActor struct CookistApp: App {
    @StateObject private var model = CookistModel()
    var body: some Scene {
        WindowGroup { RootView().environmentObject(model).tint(orange) }
    }
}
@MainActor struct RootView: View {
    @EnvironmentObject var model: CookistModel
    @Environment(\.scenePhase) private var phase
    @State private var tab = 0
    var body: some View {
        TabView(selection: Binding(get: { tab }, set: { if !model.checkoutPending { tab = $0 } })) {
            NavigationStack { ExploreView() }.tabItem { Label("Explore", systemImage: "fork.knife") }.tag(0)
            NavigationStack { BasketView() }.tabItem { Label("Basket", systemImage: "bag") }.tag(1)
            NavigationStack { OrdersView() }.tabItem { Label("Orders", systemImage: "shippingbox") }.tag(2)
            NavigationStack { DemoView() }.tabItem { Label("Demo", systemImage: "person.crop.circle") }.tag(3)
        }
        .interactiveDismissDisabled(model.checkoutPending)
        .safeAreaInset(edge: .top) {
            VStack(spacing: 2) {
                HStack { Text("Cookist").font(.largeTitle.bold()).foregroundStyle(orange); Spacer(); Text(model.role).font(.caption) }
                Text("A Machi service").font(.caption.weight(.semibold)).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading)
                Text("Good food. Your neighborhood.").font(.caption).frame(maxWidth: .infinity, alignment: .leading)
                if model.busy { ProgressView().frame(maxWidth: .infinity) }
            }.padding(.horizontal).padding(.bottom, 8).background(cream)
        }
        .task { model.load() }
        .task(id: "\(phase == .active)-\(tab)") {
            guard phase == .active, tab == 2 else { return }
            while !Task.isCancelled {
                do { try await Task.sleep(for: .seconds(5)) } catch { return }
                guard !Task.isCancelled else { return }
                model.action { try await model.refresh() }
            }
        }
        .alert("Couldn't complete request", isPresented: Binding(get: { model.error != nil }, set: { if !$0 { model.error = nil } })) {
            Button("OK") { model.error = nil }
        } message: { Text(model.error ?? "") }
    }
}
@MainActor struct ExploreView: View {
    @EnvironmentObject var model: CookistModel
    @State private var query = ""
    @State private var sort = "relevance"
    @State private var open = false
    var body: some View {
        List {
            Section {
                TextField("Find your next favorite", text: $query)
                Picker("Sort", selection: $sort) {
                    Text("Relevant").tag("relevance"); Text("Rating").tag("rating"); Text("Fastest").tag("eta"); Text("Delivery fee").tag("fee")
                }
                Toggle("Open kitchens only", isOn: $open)
                Button("Search kitchens") { model.search(query, sort: sort, open: open) }.disabled(model.busy)
            }
            if model.stores.isEmpty { Text("No kitchens found. Try another search or check your server in Demo.") }
            ForEach(model.stores) { store in
                NavigationLink { MenuView(storeID: store.id) } label: {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("\(store.emoji) \(store.name)").font(.title2.bold())
                        Text("\(store.cuisine) • ★ \(store.rating, specifier: "%.1f") • \(store.etaMinutes) min")
                        Text("\(money(store.deliveryFeeCents)) delivery • \(store.isOpen ? "Open" : "Closed")").foregroundStyle(orange)
                        Text(store.description).font(.subheadline).foregroundStyle(.secondary)
                    }.padding(.vertical, 8)
                }
            }
        }.scrollContentBackground(.hidden).background(cream).navigationTitle("Neighborhood kitchens").navigationBarTitleDisplayMode(.inline)
    }
}
@MainActor struct MenuView: View {
    @EnvironmentObject var model: CookistModel
    let storeID: String
    var store: Store? { model.stores.first { $0.id == storeID } }
    var body: some View {
        List {
            if let store {
                Section {
                    Text(store.description)
                    Text(store.isOpen ? "Kitchen open" : "Kitchen closed")
                    if model.role == "merchant", model.session?.actorId == "merchant-\(store.id)" {
                        Button(store.isOpen ? "Close kitchen" : "Open kitchen") { model.changeStore(store) }.disabled(model.busy)
                    }
                } header: { Text("\(store.emoji) \(store.name)") }
                ForEach(store.menu) { item in
                    VStack(alignment: .leading, spacing: 8) {
                        Text("\(item.emoji) \(item.name)").font(.headline)
                        Text(item.description).foregroundStyle(.secondary)
                        Text("\(item.category) • \(money(item.priceCents))")
                        if !item.available { Text("Currently unavailable").foregroundStyle(.secondary) }
                        if model.role == "merchant", model.session?.actorId == "merchant-\(store.id)" {
                            Button(item.available ? "Mark unavailable" : "Make available") { model.changeItem(item) }.disabled(model.busy)
                        } else if model.role == "customer" || model.role == "guest" {
                            HStack {
                                Button { model.quantity(store, item, delta: -1) } label: { Image(systemName: "minus.circle") }
                                    .disabled(model.busy || model.cartStore?.id != store.id)
                                Text("\(model.cartStore?.id == store.id ? model.cart[item.id] ?? 0 : 0)").monospacedDigit()
                                Button { model.quantity(store, item, delta: 1) } label: { Image(systemName: "plus.circle.fill") }
                                    .disabled(model.busy || !store.isOpen || !item.available)
                            }.buttonStyle(.borderless)
                        }
                    }.padding(.vertical, 6)
                }
            } else { Text("Kitchen no longer in these search results. Go back and search again.") }
        }.scrollContentBackground(.hidden).background(cream).navigationTitle("Menu")
    }
}
@MainActor struct BasketView: View {
    @EnvironmentObject var model: CookistModel
    var subtotal: Int {
        model.cartStore?.menu.reduce(0) { $0 + $1.priceCents * (model.cart[$1.id] ?? 0) } ?? 0
    }
    var body: some View {
        Form {
            if let store = model.cartStore, !model.cart.isEmpty {
                Section(store.name) {
                    ForEach(store.menu.filter { model.cart[$0.id] != nil }) { item in
                        HStack {
                            Text("\(item.name) × \(model.cart[item.id] ?? 0)")
                            Spacer()
                            Button("Remove one") { model.quantity(store, item, delta: -1) }.disabled(model.busy)
                        }
                    }
                    Text("Subtotal \(money(subtotal)) • Tax \(money((subtotal + 5) / 10))")
                    Text("Delivery \(money(store.deliveryFeeCents)) • Service $1.49")
                    Text("Estimated total \(money(subtotal + (subtotal + 5) / 10 + store.deliveryFeeCents + 149))").bold()
                    Text("Server calculates final prices. No real payment is collected.").font(.caption)
                }
                Section("Simulated checkout") {
                    TextField("Delivery address (8–300 characters)", text: $model.checkoutAddress, axis: .vertical).disabled(model.busy)
                    Toggle("Simulate declined card", isOn: $model.checkoutDecline).disabled(model.busy)
                    Button("Place simulated order") { model.checkout(address: model.checkoutAddress, decline: model.checkoutDecline) }
                        .disabled(model.busy || model.role != "customer" || !(8...300).contains(model.checkoutAddress.trimmingCharacters(in: .whitespacesAndNewlines).count))
                    if model.role != "customer" { Text("Select a customer identity in Demo before checkout.") }
                    Button("Clear basket", role: .destructive) { model.clearCart() }.disabled(model.busy)
                }
            } else { Text("Something delicious is waiting in Explore.") }
        }.scrollContentBackground(.hidden).background(cream).navigationTitle("Your basket")
    }
}
@MainActor struct OrdersView: View {
    @EnvironmentObject var model: CookistModel
    var body: some View {
        List {
            Button("Refresh orders") { model.action { try await model.refresh() } }.disabled(model.busy)
            if model.session == nil { Text("Choose a demo identity to view orders.") }
            else if model.orders.isEmpty { Text("No orders yet.") }
            ForEach(model.orders) { order in
                NavigationLink { OrderView(orderID: order.id) } label: {
                    VStack(alignment: .leading, spacing: 6) {
                        Text(order.storeName).font(.headline)
                        Text("\(order.status) • \(money(order.totals.totalCents))")
                        Text(order.deliveryAddress).font(.caption)
                    }
                }
            }
        }.scrollContentBackground(.hidden).background(cream).navigationTitle(model.role == "courier" ? "Jobs & deliveries" : "Your orders")
    }
}
@MainActor struct OrderView: View {
    @EnvironmentObject var model: CookistModel
    let orderID: String
    @State private var lat = "47.607"
    @State private var lng = "-122.337"
    private var coordinate: (Double, Double)? {
        guard let a = Double(lat), let b = Double(lng), a.isFinite, b.isFinite, (-90...90).contains(a), (-180...180).contains(b) else { return nil }
        return (a, b)
    }
    var body: some View {
        List {
            if let order = model.orders.first(where: { $0.id == orderID }) {
                Section(order.storeName) {
                    Text(order.status).font(.title2.bold()).foregroundStyle(orange)
                    Text("#\(order.id)").font(.caption)
                    Text("\(order.customerName) • \(order.deliveryAddress)")
                    ForEach(order.items, id: \.menuItemId) { Text("\($0.quantity) × \($0.name) • \(money($0.unitPriceCents)) each") }
                    Text("Subtotal \(money(order.totals.subtotalCents)) • Tax \(money(order.totals.taxCents))")
                    Text("Delivery \(money(order.totals.deliveryFeeCents)) • Service \(money(order.totals.serviceFeeCents))")
                    Text("Total \(money(order.totals.totalCents))").bold()
                    Text("Simulated payment: \(order.payment.status)")
                }
                Section("Delivery tracking • simulation") {
                    Text("Courier: \(order.courierName ?? "Not assigned")")
                    if let position = order.location {
                        Text("Manual position: \(position.lat, specifier: "%.5f"), \(position.lng, specifier: "%.5f")")
                        Text(position.updatedAt).font(.caption)
                    } else { Text("No simulated location yet.") }
                    ForEach(Array(order.events.enumerated()), id: \.offset) { _, event in
                        VStack(alignment: .leading) {
                            Text(event.status).bold()
                            Text(event.note)
                            Text(event.at).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
                Section("Actions") {
                    let assigned = order.courierId == model.session?.actorId
                    if model.role == "courier", order.status == "ready", order.courierId == nil {
                        Button("Claim delivery") { model.mutateOrder(order.id, suffix: "claim", body: [:]) }.disabled(model.busy)
                    }
                    ForEach(transitions(role: model.role, status: order.status, assigned: assigned), id: \.self) { next in
                        Button(next.replacingOccurrences(of: "_", with: " ").capitalized) { model.mutateOrder(order.id, suffix: "status", body: ["status": next]) }.disabled(model.busy)
                    }
                    if model.role == "courier", assigned, ["ready", "picked_up"].contains(order.status) {
                        Text("Manual simulation only. This app never collects GPS.")
                        TextField("Latitude", text: $lat).keyboardType(.numbersAndPunctuation)
                        TextField("Longitude", text: $lng).keyboardType(.numbersAndPunctuation)
                        Button("Send simulated position") {
                            if let (a, b) = coordinate { model.mutateOrder(order.id, suffix: "location", body: ["lat": a, "lng": b]) }
                        }.disabled(model.busy || coordinate == nil)
                    }
                }
            } else { Text("Order no longer available for this identity.") }
        }.scrollContentBackground(.hidden).background(cream).navigationTitle("Order details")
    }
}
@MainActor struct DemoView: View {
    @EnvironmentObject var model: CookistModel
    @State private var server = ""
    var body: some View {
        Form {
            Section("Demo, not production authentication") {
                Text("Current identity: \(model.session?.actorId ?? "Public browsing")")
                accountSection("Customers", role: "customer", accounts: model.accounts.customers)
                accountSection("Merchants", role: "merchant", accounts: model.accounts.merchants)
                accountSection("Couriers", role: "courier", accounts: model.accounts.couriers)
                Button("Reload accounts & catalog") { model.load() }.disabled(model.busy)
            }
            Section("Gateway settings") {
                TextField("http://localhost:8080", text: $server).textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.URL)
                Text("Simulator default: localhost:8080. Device: use a reachable HTTPS gateway or a local hostname permitted by App Transport Security. Changing server resets your demo session.").font(.caption)
                Button("Save server & reset session") { model.saveServer(server) }.disabled(model.busy)
            }
        }.onAppear { server = model.baseURL }.scrollContentBackground(.hidden).background(cream).navigationTitle("Demo & settings")
    }
    private func accountSection(_ title: String, role: String, accounts: [Account]) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.headline)
            ForEach(accounts) { account in
                Button("\(account.name) • \(account.id)") { model.login(role, actor: account.id) }.disabled(model.busy)
            }
        }
    }
}
