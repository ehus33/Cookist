# Cookist for Android

Native Kotlin/Jetpack Compose, Java 17, Android SDK 35, min SDK 26. Open this directory in Android Studio, or from the repository root in PowerShell:

```powershell
$env:ANDROID_HOME = "$env:LOCALAPPDATA\Android\Sdk"
.\scripts\gradle.ps1 -ProjectDir apps\android assembleDebug
```

The repository bootstrap `scripts\gradle.ps1` resolves/downloads Gradle 8.13. If present, `.\.tools\gradle-8.13\bin\gradle.bat -p apps\android assembleDebug` is equivalent. This project pins AGP 8.9.2/Kotlin 2.1.20; no global Gradle is needed. APK: `app\build\outputs\apk\debug\app-debug.apk`.

Run the gateway on port 8080. Emulator default: `http://10.0.2.2:8080`. Physical device over USB: `adb reverse tcp:8080 tcp:8080`, then set `http://localhost:8080` in Demo. Base URL is editable; HTTPS works for other hosts. Only debug builds permit HTTP, restricted to localhost, 127.0.0.1 and 10.0.2.2. Release requires HTTPS.

The gateway defaults to loopback. If emulator networking cannot reach it, prefer `adb reverse tcp:8080 tcp:8080` (also works on an emulator) with `http://localhost:8080`. Alternatively start the gateway with `$env:HOST="0.0.0.0"` on a trusted local development machine, checking Windows firewall access, then keep the emulator URL at `http://10.0.2.2:8080`. Never expose this demo-auth gateway to an untrusted network.

Explore is public. Demo fetches customer/merchant/courier identities and creates sessions with `/api/sessions`. Customers can search/menu/basket/address/simulated checkout/decline/own orders/tracking/cancel pending. Merchants can open or close their kitchen, set menu availability and advance/cancel orders. Couriers can claim ready jobs, pick up, deliver, and submit manual coordinates.

All payments and location updates are explicitly simulated. No card numbers, GPS, or private APIs. Integer-cent prices; server owns final totals. Session and basket persist locally. Orders refresh every five seconds only while the app is STARTED and Orders is selected. All operations are serialized; errors are shown in a dialog. Unchanged checkout retries reuse their idempotency key for the running process.

Checkout snapshots the cart, address, and simulated payment choice synchronously. Switching decline/approval creates a new key; identical retries (including declined attempts) retain the key. Checkout fields, tab switching and back dismissal are blocked while pending. Draft fields and retry attempts live in the ViewModel across view remounts/configuration changes, not process termination. Networking does not log request bodies, tokens, or addresses. Focused regression tests: `.\scripts\gradle.ps1 -ProjectDir apps\android testDebugUnitTest --tests com.cookist.app.CheckoutRetryTest`.
