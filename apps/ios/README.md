# Cookist for iOS

Native SwiftUI app shell, iOS 17+, Xcode 15+. On macOS with [XcodeGen](https://github.com/yonaskolb/XcodeGen) installed:

```sh
cd apps/ios
xcodegen generate
open Cookist.xcodeproj
```

Select an iPhone simulator and run the Cookist scheme.

No signing team is necessary for simulator builds; set your own team for a physical device. Project and Info.plist are generated from `project.yml`. **Not compiled or run on Windows**; macOS/Xcode validation is required.
The app currently provides a simple, neutral Cookist-branded shell. Existing local app data is not accessed or cleared.
