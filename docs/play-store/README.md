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
Audience: amateur photographers and families who take a lot of photos. The text speaks to them
first (full-quality originals, finding a photo, sharing with family) and keeps the device and
privacy details to the end.

- **Short description** (80):
  `Every family photo in full quality, at home, shared only with who you choose.`
- **Full description**:

  > Off The Cloud is a home for your photos: every shot from your phone and your camera, kept in
  > full quality on a small device in your own home instead of a company's cloud. No storage plan
  > to outgrow, no compression, no one else looking through your family's pictures.
  >
  > Made for people who take a lot of photos:
  >
  > - Every photo and video on your phone backed up automatically, in the background, as the
  >   original.
  > - Your camera's files too, RAW included, in folders that keep earlier versions.
  > - Find any picture in seconds: search by what is in it, where it was taken, or who is in it
  >   (face recognition is optional and off by default).
  > - See each photo's camera details, and where it was taken on a map.
  > - Share an album with grandparents and friends as a link that expires. They open it in their
  >   browser, with no account or app, and can download the originals.
  > - A private feed for family and friends: posts, comments and likes go from device to device,
  >   never through a company's servers.
  > - At home, photos and videos move over your own Wi-Fi, so a whole holiday backs up quickly
  >   instead of crawling over the internet.
  > - Set up a new device over Bluetooth, straight from the app.
  >
  > Your photos stay yours. They are encrypted on your device and mirrored on two disks, so one
  > disk failing loses nothing. Away from home the app reaches your device through our relay over
  > an encrypted connection, and the relay keeps none of your photos. Prefer that nothing passes
  > through us? Use it at home, through Tailscale Funnel, or with a relay of your own.
  >
  > Off The Cloud needs an Off The Cloud device: a Raspberry Pi with two disks running our
  > open-source server. The server and the apps are open source (AGPL).

- **Release notes** (first release):
  `The first Off The Cloud for Android: back up every family photo in full quality to your own device at home, find any picture in seconds, and share albums with the people you love.`

- **Category**: Photography (alternatively Productivity). **Tags**: backup, photos, storage.
- **Contact email**: the owner's support address. **Website**: `https://off-the.cloud`.
- **Privacy policy URL**: `https://off-the.cloud/privacy` (issue #175). The same page serves the
  App Store entry.

## Graphics

- App icon, 512 x 512: `docs/play-store/icon-512.png`. It is the iOS icon, made from the same
  source as the adaptive launcher icon (`mipmap-anydpi-v26/ic_launcher.xml`: the artwork as the
  foreground layer over `#08373F`).
- Feature graphic, 1024 x 500: `docs/play-store/feature-graphic.png`.
- Phone screenshots: `docs/play-store/screenshots/` - 1-library, 2-people (search by face),
  3-viewer, 4-share-gallery, 5-friends-feed; `alt-library-beach` is an alternative first shot
  (its last row shows a child). Taken from the Fold's cover screen against Pit (the demo device,
  photos of people who consented), status and gesture bars cropped, padded to Play's 2:1
  maximum (1142 x 2284).

## Data safety

Answers as the app works today. Play counts data as "collected" when it leaves the phone, even to
a server the user owns, unless one of Play's exemptions applies. Check each answer against the
current Play Console wording before submitting.

- **Encrypted in transit**: yes. At home the app connects straight to the device over TLS
  pinned to the device's own certificate (#190), so nothing passes through us. Away from home it
  is TLS on both legs through the relay, which is not end-to-end: the relay decrypts and
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
