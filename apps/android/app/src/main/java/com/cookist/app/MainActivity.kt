package com.cookist.app

import android.app.Application
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import kotlinx.coroutines.*
import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URI
import java.net.URLEncoder

private fun JSONArray.objects() = (0 until length()).map { getJSONObject(it) }
private fun JSONObject.text(key: String) = if (isNull(key)) "" else optString(key)
private fun money(cents: Int) = "$" + "%d.%02d".format(cents / 100, cents % 100)
private fun obj(vararg pairs: Pair<String, Any>) = JSONObject(mapOf(*pairs))

class CookistModel(app: Application) : AndroidViewModel(app) {
    private val prefs = app.getSharedPreferences("cookist", 0)
    var baseUrl by mutableStateOf(prefs.getString("baseUrl", "http://10.0.2.2:8080")!!)
    var session by mutableStateOf(prefs.getString("session", null)?.let { JSONObject(it) })
    var stores by mutableStateOf(emptyList<JSONObject>())
    var orders by mutableStateOf(emptyList<JSONObject>())
    var accounts by mutableStateOf(JSONObject())
    var error by mutableStateOf<String?>(null)
    var busy by mutableStateOf(false)
    var cart by mutableStateOf(prefs.getString("cart", "{}")!!.let { JSONObject(it) })
    var cartStore by mutableStateOf(prefs.getString("cartStore", "")!!)
    var cartSnapshot by mutableStateOf(prefs.getString("cartSnapshot", null)?.let { JSONObject(it) })
    var selectedStore by mutableStateOf<String?>(null)
    private val checkoutRetry = CheckoutRetry()
    var checkoutPending by mutableStateOf(false)
        private set
    var checkoutAddress by mutableStateOf("")
    var checkoutDecline by mutableStateOf(false)
    val role get() = session?.text("role") ?: "guest"
    val actor get() = session?.text("actorId") ?: ""

    private suspend fun request(path: String, method: String = "GET", body: JSONObject? = null): String = withContext(Dispatchers.IO) {
        val connection = URI(baseUrl.trimEnd('/') + path).toURL().openConnection() as HttpURLConnection
        try {
            connection.requestMethod = method
            connection.connectTimeout = 8000
            connection.readTimeout = 10000
            session?.text("token")?.let { connection.setRequestProperty("Authorization", "Bearer $it") }
            if (body != null) {
                connection.doOutput = true
                connection.setRequestProperty("Content-Type", "application/json")
                connection.outputStream.use { it.write(body.toString().toByteArray(Charsets.UTF_8)) }
            }
            val status = connection.responseCode
            val stream = if (status in 200..299) connection.inputStream else connection.errorStream
            val text = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
            if (status !in 200..299) {
                val message = runCatching { JSONObject(text).getJSONObject("error").getString("message") }.getOrDefault(text)
                throw IllegalStateException("HTTP $status: $message")
            }
            text
        } finally { connection.disconnect() }
    }
    fun action(block: suspend () -> Unit) {
        if (busy) return
        busy = true
        viewModelScope.launch {
            try { block() } catch (e: Exception) {
                if (e is CancellationException) throw e
                error = e.localizedMessage ?: "Request failed"
            } finally { busy = false }
        }
    }
    suspend fun refresh() {
        val freshStores = JSONArray(request("/api/stores")).objects()
        val freshOrders = if (session != null) JSONArray(request("/api/orders")).objects() else emptyList()
        stores = freshStores
        orders = freshOrders
        freshStores.find { it.text("id") == cartStore }?.let {
            cartSnapshot = it
            persistCart()
        }
    }
    fun load() = action {
        accounts = JSONObject(request("/api/demo-accounts"))
        refresh()
    }
    fun search(query: String, sort: String, open: Boolean) = action {
        stores = JSONObject(request("/api/search?q=${URLEncoder.encode(query, "UTF-8")}&sort=$sort&openOnly=$open")).getJSONArray("stores").objects()
    }
    fun login(role: String, id: String) = action {
        session = JSONObject(request("/api/sessions", "POST", obj("role" to role, "actorId" to id)))
        prefs.edit().putString("session", session.toString()).apply()
        checkoutRetry.reset()
        orders = emptyList()
        refresh()
    }
    fun settings(url: String) {
        if (busy) return
        runCatching {
            val uri = URI(url.trim())
            require(uri.scheme in listOf("http", "https") && !uri.host.isNullOrBlank() && uri.userInfo == null && uri.query == null && uri.fragment == null && uri.path.orEmpty() in listOf("", "/")) { "Enter an http(s) server origin without a path." }
            baseUrl = url.trim().trimEnd('/')
            session = null
            orders = emptyList()
            prefs.edit().putString("baseUrl", baseUrl).remove("session").apply()
            checkoutRetry.reset()
            load()
        }.onFailure { error = it.message }
    }
    fun quantity(store: JSONObject, item: JSONObject, delta: Int) {
        if (busy) return
        if (cartStore.isNotEmpty() && cartStore != store.text("id") && cart.length() > 0) {
            error = "Your basket is from another kitchen. Clear it first."; return
        }
        val next = JSONObject(cart.toString())
        val quantity = (next.optInt(item.text("id")) + delta).coerceIn(0, 20)
        if (quantity == 0) next.remove(item.text("id")) else next.put(item.text("id"), quantity)
        cartStore = store.text("id")
        cartSnapshot = store
        cart = next
        persistCart()
    }
    private fun persistCart() {
        prefs.edit().putString("cart", cart.toString()).putString("cartStore", cartStore).putString("cartSnapshot", cartSnapshot?.toString()).apply()
    }
    fun clearCart() { if (!busy) resetCart() }
    private fun resetCart() { cart = JSONObject(); cartStore = ""; cartSnapshot = null; persistCart() }
    fun checkout(address: String, decline: Boolean) {
        if (busy) return
        val snapshot = cart.keys().asSequence().associateWith { cart.getInt(it) }
        val attempt = checkoutRetry.prepare(cartStore, snapshot, address, decline)
        val items = JSONArray()
        attempt.draft.items.forEach { items.put(obj("menuItemId" to it.menuItemId, "quantity" to it.quantity)) }
        val body = obj("storeId" to attempt.draft.storeId, "items" to items, "deliveryAddress" to attempt.draft.deliveryAddress, "paymentMethod" to attempt.draft.paymentMethod, "idempotencyKey" to attempt.key)
        checkoutPending = true
        action {
            try {
                request("/api/orders", "POST", body)
                resetCart()
                checkoutRetry.reset()
                refresh()
            } finally { checkoutPending = false }
        }
    }
    fun mutate(path: String, body: JSONObject, method: String = "POST") = action {
        request(path, method, body)
        refresh()
    }
}

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            MaterialTheme(colorScheme = lightColorScheme(primary = Color(0xFFD65021), background = Color(0xFFFFF8F1), surface = Color(0xFFFFF8F1))) {
                Cookist()
            }
        }
    }
}

@Composable private fun Cookist(model: CookistModel = viewModel()) {
    var tab by remember { mutableStateOf("Explore") }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    BackHandler(enabled = model.checkoutPending) { }
    LaunchedEffect(Unit) { model.load() }
    LaunchedEffect(lifecycle, tab, model.session, model.baseUrl) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (isActive) {
                delay(5000)
                if (tab == "Orders" && !model.busy) model.action { model.refresh() }
            }
        }
    }
    Scaffold(
        topBar = { Column(Modifier.padding(20.dp)) {
            Text("Cookist", style = MaterialTheme.typography.headlineLarge, color = MaterialTheme.colorScheme.primary)
            Text("A Machi service", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            Text("Good food. Your neighborhood. • ${model.role}")
            if (model.busy) LinearProgressIndicator(Modifier.fillMaxWidth())
        } },
        bottomBar = { NavigationBar {
            listOf("Explore", "Basket", "Orders", "Demo").forEach { name ->
                NavigationBarItem(selected = tab == name, onClick = { tab = name }, enabled = !model.checkoutPending, icon = { Text(when(name) { "Explore" -> "⌂"; "Basket" -> "▣"; "Orders" -> "↗"; else -> "☺" }) }, label = { Text(name) })
            }
        } }
    ) { padding ->
        Column(Modifier.padding(padding).padding(horizontal = 16.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            when (tab) {
                "Explore" -> Explore(model)
                "Basket" -> Basket(model)
                "Orders" -> {
                    Text(if (model.role == "courier") "Jobs & deliveries" else "Your orders", style = MaterialTheme.typography.headlineSmall)
                    Button(onClick = { model.action { model.refresh() } }, enabled = !model.busy) { Text("Refresh") }
                    if (model.session == null) Text("Choose a demo identity to view orders.")
                    else if (model.orders.isEmpty()) Text("No orders yet.")
                    model.orders.forEach { order -> key(order.text("id")) { OrderCard(model, order) } }
                }
                else -> DemoSettings(model)
            }
            Spacer(Modifier.height(20.dp))
        }
    }
    model.error?.let { message -> AlertDialog(onDismissRequest = { model.error = null }, title = { Text("Couldn't complete request") }, text = { Text(message) }, confirmButton = { TextButton(onClick = { model.error = null }) { Text("OK") } }) }
}

@Composable private fun Explore(model: CookistModel) {
    var query by remember { mutableStateOf("") }
    var sort by remember { mutableStateOf("relevance") }
    var open by remember { mutableStateOf(false) }
    OutlinedTextField(query, { query = it.take(100) }, label = { Text("Find your next favorite") }, modifier = Modifier.fillMaxWidth())
    Row { Checkbox(open, { open = it }); Text("Open kitchens only", Modifier.padding(top = 12.dp)) }
    Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
        listOf("relevance", "rating", "eta", "fee").forEach { value -> FilterChip(selected = sort == value, onClick = { sort = value }, label = { Text(value) }) }
    }
    Button(onClick = { model.search(query, sort, open) }, enabled = !model.busy) { Text("Search kitchens") }
    if (model.stores.isEmpty()) Text("No kitchens found. Try another search or check your server in Demo.")
    model.stores.forEach { store ->
        Card(Modifier.fillMaxWidth()) { Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("${store.text("emoji")} ${store.text("name")}", style = MaterialTheme.typography.titleLarge)
            Text("${store.text("cuisine")} • ★ ${store.optDouble("rating")} • ${store.optInt("etaMinutes")} min")
            Text("${money(store.optInt("deliveryFeeCents"))} delivery • ${if(store.optBoolean("isOpen")) "Open" else "Closed"}")
            Text(store.text("description"))
            TextButton(onClick = { model.selectedStore = if(model.selectedStore == store.text("id")) null else store.text("id") }) { Text(if (model.selectedStore == store.text("id")) "Hide menu" else "View menu") }
            val own = model.role == "merchant" && model.actor == "merchant-${store.text("id")}"
            if (own) Button(onClick = { model.mutate("/api/stores/${store.text("id")}/availability", obj("isOpen" to !store.optBoolean("isOpen")), "PATCH") }, enabled = !model.busy) { Text(if(store.optBoolean("isOpen")) "Close kitchen" else "Open kitchen") }
            if (model.selectedStore == store.text("id")) store.getJSONArray("menu").objects().forEach { item ->
                HorizontalDivider()
                Text("${item.text("emoji")} ${item.text("name")} • ${money(item.optInt("priceCents"))}", style = MaterialTheme.typography.titleMedium)
                Text(item.text("description"))
                if (!item.optBoolean("available")) Text("Currently unavailable")
                if (own) TextButton(onClick = { model.mutate("/api/menu-items/${item.text("id")}", obj("available" to !item.optBoolean("available")), "PATCH") }, enabled = !model.busy) { Text(if(item.optBoolean("available")) "Mark unavailable" else "Make available") }
                if (model.role in listOf("customer", "guest")) Row {
                    TextButton(onClick = { model.quantity(store, item, -1) }, enabled = !model.busy && model.cartStore == store.text("id")) { Text("−") }
                    Text("${if(model.cartStore == store.text("id")) model.cart.optInt(item.text("id")) else 0}", Modifier.padding(12.dp))
                    TextButton(onClick = { model.quantity(store, item, 1) }, enabled = !model.busy && store.optBoolean("isOpen") && item.optBoolean("available")) { Text("+") }
                }
            }
        } }
    }
}

@Composable private fun Basket(model: CookistModel) {
    Text("Your basket", style = MaterialTheme.typography.headlineSmall)
    val store = model.cartSnapshot
    if (model.cart.length() == 0) { Text("Something delicious is waiting in Explore."); return }
    Text(store?.text("name") ?: model.cartStore)
    var subtotal = 0
    model.cart.keys().forEach { id ->
        val item = store?.getJSONArray("menu")?.objects()?.find { it.text("id") == id }
        val count = model.cart.getInt(id)
        subtotal += (item?.optInt("priceCents") ?: 0) * count
        Row {
            Text("${item?.text("name") ?: id} × $count", Modifier.weight(1f))
            if (store != null && item != null) TextButton(onClick = { model.quantity(store, item, -1) }, enabled = !model.busy) { Text("Remove one") }
        }
    }
    if (store != null) {
        val tax = (subtotal + 5) / 10
        Text("Estimated subtotal ${money(subtotal)} • tax ${money(tax)}")
        Text("Delivery ${money(store.optInt("deliveryFeeCents"))} • service $1.49")
        Text("Estimated total ${money(subtotal + tax + store.optInt("deliveryFeeCents") + 149)}")
    }
    Text("Server calculates final prices. No real payment is collected.")
    OutlinedTextField(model.checkoutAddress, { model.checkoutAddress = it.take(300) }, enabled = !model.busy, label = { Text("Delivery address (8–300 characters)") }, modifier = Modifier.fillMaxWidth())
    Row { Checkbox(model.checkoutDecline, { model.checkoutDecline = it }, enabled = !model.busy); Text("Simulate a declined card", Modifier.padding(top = 12.dp)) }
    Button(onClick = { model.checkout(model.checkoutAddress, model.checkoutDecline) }, enabled = !model.busy && model.role == "customer" && model.checkoutAddress.trim().length in 8..300) { Text("Place simulated order") }
    if (model.role != "customer") Text("Choose a customer identity in Demo before checkout.")
    TextButton(onClick = { model.clearCart() }, enabled = !model.busy) { Text("Clear basket") }
}

@Composable private fun OrderCard(model: CookistModel, order: JSONObject) {
    var lat by remember { mutableStateOf("47.607") }
    var lng by remember { mutableStateOf("-122.337") }
    val id = order.text("id")
    val status = order.text("status")
    val assigned = order.text("courierId") == model.actor
    Card(Modifier.fillMaxWidth()) { Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(order.text("storeName"), style = MaterialTheme.typography.titleLarge)
        Text("$status • #$id")
        Text("${order.text("customerName")} • ${order.text("deliveryAddress")}")
        order.getJSONArray("items").objects().forEach { Text("${it.optInt("quantity")} × ${it.text("name")}") }
        val totals = order.getJSONObject("totals")
        Text("Subtotal ${money(totals.optInt("subtotalCents"))} • Tax ${money(totals.optInt("taxCents"))}")
        Text("Delivery ${money(totals.optInt("deliveryFeeCents"))} • Service ${money(totals.optInt("serviceFeeCents"))}")
        Text("Total ${money(totals.optInt("totalCents"))}", style = MaterialTheme.typography.titleMedium)
        Text("Simulated payment: ${order.getJSONObject("payment").text("status")}")
        Text("Courier: ${order.text("courierName").ifBlank { "Not assigned" }}")
        order.optJSONObject("location")?.let { Text("Simulated position: ${it.optDouble("lat")}, ${it.optDouble("lng")} • ${it.text("updatedAt")}") } ?: Text("No simulated location yet.")
        order.getJSONArray("events").objects().forEach { Text("${it.text("status")} • ${it.text("at")}\n${it.text("note")}", style = MaterialTheme.typography.bodySmall) }
        if(model.role == "courier" && status == "ready" && order.text("courierId").isBlank()) Button(onClick = { model.mutate("/api/orders/$id/claim", obj()) }, enabled = !model.busy) { Text("Claim delivery") }
        val transitions = when(model.role) {
            "customer" -> if(status == "pending") listOf("cancelled") else emptyList()
            "merchant" -> when(status) { "pending" -> listOf("accepted", "cancelled"); "accepted" -> listOf("preparing", "cancelled"); "preparing" -> listOf("ready", "cancelled"); "ready" -> listOf("cancelled"); else -> emptyList() }
            "courier" -> if(assigned) when(status) { "ready" -> listOf("picked_up"); "picked_up" -> listOf("delivered"); else -> emptyList() } else emptyList()
            else -> emptyList()
        }
        transitions.forEach { next -> Button(onClick = { model.mutate("/api/orders/$id/status", obj("status" to next)) }, enabled = !model.busy) { Text(next.replace('_', ' ')) } }
        if(model.role == "courier" && assigned && status in listOf("ready", "picked_up")) {
            Text("Manual location simulation • not GPS")
            OutlinedTextField(lat, { lat = it }, label = { Text("Latitude") })
            OutlinedTextField(lng, { lng = it }, label = { Text("Longitude") })
            val latitude = lat.toDoubleOrNull()
            val longitude = lng.toDoubleOrNull()
            Button(onClick = { model.mutate("/api/orders/$id/location", obj("lat" to latitude!!, "lng" to longitude!!)) }, enabled = !model.busy && latitude != null && longitude != null && latitude.isFinite() && longitude.isFinite() && latitude in -90.0..90.0 && longitude in -180.0..180.0) { Text("Send simulated position") }
        }
    } }
}

@Composable private fun DemoSettings(model: CookistModel) {
    var url by remember(model.baseUrl) { mutableStateOf(model.baseUrl) }
    Text("Demo, not production authentication", style = MaterialTheme.typography.titleLarge)
    Text("Current identity: ${model.actor.ifBlank { "Public browsing" }}")
    listOf("customers" to "customer", "merchants" to "merchant", "couriers" to "courier").forEach { (key, role) ->
        Text(role.replaceFirstChar { it.uppercase() }, style = MaterialTheme.typography.titleMedium)
        model.accounts.optJSONArray(key)?.objects()?.forEach { account ->
            OutlinedButton(onClick = { model.login(role, account.text("id")) }, enabled = !model.busy) { Text("${account.text("name")} • ${account.text("id")}") }
        }
    }
    Button(onClick = { model.load() }, enabled = !model.busy) { Text("Reload accounts & catalog") }
    OutlinedTextField(url, { url = it }, label = { Text("Gateway base URL") }, modifier = Modifier.fillMaxWidth())
    Text("Emulator: http://10.0.2.2:8080. Device USB: adb reverse tcp:8080 tcp:8080, then http://localhost:8080. HTTP allowed only for these loopback hosts in debug.")
    Button(onClick = { model.settings(url) }, enabled = !model.busy) { Text("Save server & reset session") }
}
