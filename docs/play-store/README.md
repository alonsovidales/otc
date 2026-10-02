# Google Play listing - Off The Cloud for Android

What goes into Play Console for `cloud.offthe.otc` (issue #129). It mirrors the App Store Connect
entries for the iOS app; change both together.

## Building and uploading

- Upload key: `~/.otc/otc-upload.jks` (alias `otc-upload`, PKCS12, RSA 4096). The password is in
  the macOS Keychain, service `otc-android-upload`, account `otc-upload`. The keystore is never in
  the repo. **Back it up** (with its password) somewhere safe. With Play App Signing, Google keeps
  the real signing key and a lost upload key can be reset from Play Console, but that takes days.
  Upload-key certificate SHA-256:
  `BE:E2:F4:79:51:7C:E1:B0:97:46:38:8B:D8:65:E9:C0:37:39:99:3E:7D:01:D6:30:C7:04:EB:A1:A8:74:3E:58`
- Bundle for Play: `make android-release` (or by hand:)
  ```
  cd app/android
  OTC_UPLOAD_PASSWORD="$(security find-generic-password -s otc-android-upload -a otc-upload -w)" \
    ./gradlew bundleRelease
  ```
  It writes `app/build/outputs/bundle/release/app-release.aab`. Copy `app/google-services.json`
  in first (see #125), or the build has no push notifications.
- `versionCode` is the commit count (`git rev-list --count HEAD`), so every upload from a newer
  commit is higher. `versionName` is set by hand in `app/build.gradle.kts`.
- To try the minified build on a phone that has the debug build installed (same key, so the app's
  data stays): `./gradlew assembleRelease -Potc.signWithDebug`, then `adb install -r
  app/build/outputs/apk/release/app-release.apk`.
- R8: minify and resource shrinking are on for release. The generated protobuf classes are kept
  (`proguard-rules.pro`). OkHttp, Media3, Firebase, Tink (security-crypto), Coil and osmdroid ship
  their own consumer rules, and the build reports no missing classes.

## Store listing

- **App name** (30): `Off The Cloud`
- **Short description** (80):
  `Your photos and files on your own device at home, not someone else's cloud.`
- **Full description**:

  > Off The Cloud keeps your photos, videos and files on a small device in your home: a
  > Raspberry Pi with two mirrored disks running our open-source server. Nothing is stored on our
  > servers.
  >
  > This app is how you use that device from your phone:
  >
  > - Back up your phone's photos and videos automatically, in the background.
  > - Browse, search and share your library from anywhere. Search by what is in a photo, by place,
  >   or by person (face recognition is optional and off by default).
  > - Browse and upload files, with folders that keep earlier versions.
  > - Share an album as a link that expires, without the people you share with needing an account.
  > - A private social feed with your friends' own devices: posts, comments and likes go from
  >   device to device, never through a company's servers.
  > - Set up a new device over Bluetooth, straight from the app.
  >
  > Everything you upload is encrypted at rest on your device. When you are away from home the
  > app reaches it through our relay over an encrypted connection; the relay stores none of your
  > content. Prefer that nothing passes through us? Use it at home, through Tailscale Funnel, or
  > with a relay of your own.
  >
  > Off The Cloud needs an Off The Cloud device. The server and the apps are open source (AGPL).

- **Category**: Photography (alternatively Productivity). **Tags**: backup, photos, storage.
- **Contact email**: the owner's support address. **Website**: `https://off-the.cloud`.
- **Privacy policy URL**: `https://off-the.cloud/privacy` (issue #175). The same page serves the
  App Store entry.

## Graphics

- App icon, 512 x 512: `docs/play-store/icon-512.png`. It is the iOS icon, made from the same
  source as the adaptive launcher icon (`mipmap-anydpi-v26/ic_launcher.xml`: the artwork as the
  foreground layer over `#08373F`).
- Feature graphic, 1024 x 500: still to make.
- Phone screenshots, at least 2 (up to 8), 16:9 or 9:16, each side 320-3840 px: still to take.
  `docs/screenshots/` has iOS and web ones for reference. They have to come from a phone on a
  demo device with demo photos, not from a real library.

## Data safety

Answers as the app works today. Play counts data as "collected" when it leaves the phone, even to
a server the user owns, unless one of Play's exemptions applies. Check each answer against the
current Play Console wording before submitting.

- **Encrypted in transit**: yes, TLS on both legs. It is not end-to-end: the relay decrypts and
  re-encrypts, so content is readable in its memory while it passes, though never stored. The
  device password is end-to-end (RSA-OAEP to the device's key). `/privacy` says this plainly.
- **Users can request deletion**: content is deleted on the user's own device. The account is
  deleted on the account page ("Delete my account", #182), also reached from the app's Settings >
  Bridge and Account; its data can be downloaded there first.
- **Photos and videos, Files and docs**: sent to the user's own device. Away from home they pass
  through the relay, which is not end-to-end encrypted (see above) and stores none of it.
  Purpose: app functionality (backup). Not shared with
  third parties.
- **Name, email address** (only when the device is set up in the app with an account, including
  Sign in with Apple or Google): stored by the bridge for the account. Purpose: account
  management. Not shared.
- **Device or other IDs**: the Firebase Cloud Messaging token, kept by the bridge so the user's
  device can notify the phone. Purpose: app functionality (notifications). Firebase processes it
  for delivery.
- **Approximate or precise location**: photos keep their own location metadata (read with
  `ACCESS_MEDIA_LOCATION`) and it goes to the user's own device with the photo, for the map and
  place search. The app itself does not ask for the phone's location; the `ACCESS_FINE_LOCATION`
  permission is limited to Android 11 and older, where Bluetooth scanning needs it.
- **None**: no analytics, no advertising, no crash reporting SDK, and no data sold.

## App content declarations

- **Ads**: none.
- **Target audience**: 18+ (it is a personal storage and social app for device owners).
- **Content rating**: questionnaire. It has user-generated content shared with friends: the
  friend-to-friend feed, with blocking and removal of friends.
- **Account deletion**: Play requires it for an app that creates accounts (the in-app setup
  does). Done in #182: Settings > Bridge and Account > Delete My Account in the app, and the
  account page on the web. The URL to give is
  `https://off-the.cloud/account`.
- **Permissions to explain**: photos and videos (backup), media location (map and place search),
  notifications, and Bluetooth (setting up a new device).
- **App access for review**: a demo device name and password, as for App Review on iOS (see
  `docs/app-review-macos.md` for the form those notes take).
