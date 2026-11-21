# Cookist for Android

Native Kotlin/Jetpack Compose app shell, Java 17, Android SDK 35, min SDK 26. Open this directory in Android Studio, or from the repository root in PowerShell:

```powershell
$env:ANDROID_HOME = "$env:LOCALAPPDATA\Android\Sdk"
.\scripts\gradle.ps1 -ProjectDir apps\android assembleDebug
```

The repository bootstrap `scripts\gradle.ps1` resolves/downloads Gradle 8.13. If present, `.\.tools\gradle-8.13\bin\gradle.bat -p apps\android assembleDebug` is equivalent. This project pins AGP 8.9.2/Kotlin 2.1.20; no global Gradle is needed. APK: `app\build\outputs\apk\debug\app-debug.apk`.

The app currently provides a simple, neutral Cookist-branded shell. Existing local app data is not accessed or cleared.
