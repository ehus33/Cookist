# Cookist for iOS

Native SwiftUI demo, iOS 17+, Xcode 15+. On macOS with [XcodeGen](https://github.com/yonaskolb/XcodeGen) installed:

```sh
cd apps/ios
xcodegen generate
open Cookist.xcodeproj
```

Select an iPhone simulator and run the Cookist scheme. To run focused decoding/transition tests (choose a simulator installed on your Mac):

```sh
xcodebuild -project Cookist.xcodeproj -scheme Cookist -destination 'platform=iOS Simulator,name=iPhone 16' test
```

No signing team is necessary for simulator builds; set your own team for a physical device. Project and Info.plist are generated from `project.yml`. **Not compiled or run on Windows**; macOS/Xcode validation is required.

Start the shared gateway on port 8080. The simulator defaults to `http://localhost:8080`; edit the origin in Demo & settings. Local networking/localhost ATS exceptions permit development HTTP, not arbitrary remote HTTP. HTTPS is supported.

Explore is public; Demo loads identities from `/api/demo-accounts` and creates a real demo session. Customer: browse/search, build one-kitchen basket, provide an address, choose simulated approval/decline, view own orders, tracking events, and cancel pending orders. Merchant: open own kitchen/menu and advance/cancel own orders. Courier: view jobs, claim, pick up, deliver, and submit manual simulated coordinates. No card numbers, GPS, real authentication, or real charges.

Session and basket persist in UserDefaults. Requests surface network, decoding, and API errors. Checkout retries reuse an idempotency key for an unchanged payload in the running process. Order tracking polls every five seconds only while Orders is selected and the scene is active; mutations and refreshes are serialized so polling cannot overwrite an in-flight action.

Checkout snapshots the cart, address, and simulated payment choice before scheduling a request. Changing approval/decline generates a new retry key; retrying the identical request (including a decline) retains its key. Checkout fields and tab switching are locked while pending. Draft fields and the retry attempt live in the app model across view remounts, but not process termination. No full HTTP requests, tokens, or addresses are logged by the client.
