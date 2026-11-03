# Experimental mobile packaging

MyGo can package an app of native UI (`WindowOptions.Content`) for iPhone,
iPad and Android. The Go app compiles to `js/wasm`; a small UIKit/WKWebView
or Android Activity/WebView host loads the bundled page and WASM. This keeps
`CGO_ENABLED=0` and uses the existing browser backend, including its touch
and text input support. No Bun runtime, native Go library or browser engine
is bundled.

This is an experimental packaging path, not a native `GOOS=ios` or
`GOOS=android` backend. Desktop WebView apps, `frontendDist`, typed WebView
IPC and native Go plugins are not supported. Desktop-only APIs have the
same limitations as [the browser backend](architecture.md#browsers-internalweb).
The Go app runs inside the WebView and has browser filesystem, networking,
background execution and lifecycle constraints. It is not a native service
that continues running while the WebView is suspended.

## Build

From this repository:

```sh
CGO_ENABLED=0 go run ./cmd/mygo build -platform ios-simulator examples/counter-native
CGO_ENABLED=0 go run ./cmd/mygo build -platform ios examples/counter-native
CGO_ENABLED=0 go run ./cmd/mygo build -platform android -debug examples/counter-native
CGO_ENABLED=0 go run ./cmd/mygo build -platform android examples/counter-native
```

Targets have no architecture suffix: the Go payload is WASM. Xcode builds
the native iOS host for the selected SDK's architectures; the Android host
contains no ABI-specific native libraries. An iOS app supports both iPhone
and iPad, with safe-area and keyboard layout, rotation and one scene. It
does not implement iPad multiwindow restoration or native mobile plugins.

Each target writes a directory under the configured `out` (by default
`dist`, or `build` in generated projects):

| Target | Project | Compiled artifact |
| --- | --- | --- |
| `ios-simulator` | `project/MyGo.xcodeproj` | `MyGo.app`, for a simulator |
| `ios` | `project/MyGo.xcodeproj` | `MyGo.app`, **unsigned**, for devices |
| `android -debug` | `project/`, a Gradle project | `<slug>-debug.apk`, debug signed |
| `android` | `project/`, a Gradle project | `<slug>-release.apk`, **unsigned** |

A failed build leaves the previous target directory intact. A successful
build replaces it, including the generated project. Copy a project outside
the output directory before making lasting native customizations.

To generate complete host projects and their WASM payload on any desktop
without invoking Xcode or Gradle:

```sh
CGO_ENABLED=0 go run ./cmd/mygo build \
  -platform ios,android -mobile-project-only examples/counter-native
```

## iOS and iPadOS

Requires macOS and Xcode with the appropriate iOS SDK, and iOS/iPadOS 15 or
later. The host uses public UIKit, WebKit and UniformTypeIdentifiers APIs.
It serves only files in its bundled `Web` directory through a
`WKURLSchemeHandler`; it does not run a local server or grant arbitrary
file access. The loader compiles WASM bytes instead of relying on streaming
compilation for a custom URL scheme.

Install the simulator build on a booted simulator:

```sh
xcrun simctl install booted examples/counter-native/dist/ios-simulator/MyGo.app
xcrun simctl launch booted com.mygo.counternative
```

For a device or TestFlight, open the generated Xcode project, select a
signing team, configure provisioning, add app icons, set a release build
number, then build or archive/export in Xcode. The CLI does not produce a
signed IPA, manage provisioning or submit to the App Store.

## Android

Requires JDK 17, Gradle 8.13 or 8.14, Android SDK platform 36 and the Android
build tools. The generated project pins Android Gradle Plugin 8.13.2; its
first build may download plugin dependencies. Set `ANDROID_HOME` to the SDK
and `JAVA_HOME` to your JDK. Install Gradle on `PATH`, or set `MYGO_GRADLE`
to the path of its executable (not a shell command). A Gradle wrapper is
not bundled. The minimum OS is Android 8 (API 26); the system WebView must
support Go's WASM runtime and WebGL 2 (or the browser backend's CPU fallback).

The host serves bundled assets at a virtual HTTPS origin with explicit
WASM MIME types and blocks external navigation and asset requests. It
needs no Internet permission for the bundled app. Remote networking is
not enabled by this initial host. It keeps UI clear of system bars,
cutouts and the keyboard; the WebView pauses/resumes with its Activity.

Install a debug build:

```sh
adb install -r examples/counter-native/dist/android/counter-native-debug.apk
adb shell am start -n com.mygo.counternative/dev.mygo.host.MainActivity
```

For distribution, copy the generated project, configure your signing key,
app icons and release `versionCode` (initially 1), then run `gradle
bundleRelease` for an AAB or `gradle assembleRelease` for an APK. The CLI's
release APK is unsigned and cannot be installed as generated.

## Configuration and limits

The existing `name`, `identifier` and `version` configure the host. Mobile
identifiers must have at least two dot-separated ASCII alphanumeric
components, each starting with a letter. Versions must be three numeric
components, such as `1.2.3`. The initial templates use build number /
versionCode 1; update these in a copied project for subsequent store releases.

Desktop `updates` and `-upload` are rejected for mobile targets. Declared
`resources` are also rejected: embed WASM assets with Go's `embed` package.
The implicit desktop `resources/` directory, desktop signing options,
custom URL schemes, file associations and app icons are not translated to
mobile manifests. Add required mobile metadata in a copied native project.

The CLI checks the configuration for common desktop WebView inputs, but a
Go app with no `frontendDist` may still use desktop-only APIs. Use an app
known to run with `mygo build -platform web`; a successful WASM compilation
does not prove every runtime API is supported.
