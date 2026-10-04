# App Store privacy answers (iOS and macOS)

What to answer in App Store Connect > App Privacy for Off The Cloud, as the apps work today
(issue #175). It matches `PrivacyInfo.xcprivacy` in `app/ios` and `app/macos`, the privacy notice
at `https://off-the.cloud/privacy`, and the Play Console's Data safety answers in
`docs/play-store/README.md`. Check each answer against the current App Store Connect wording
before submitting, and update this file, the manifests and `/privacy` together.

**Privacy policy URL:** `https://off-the.cloud/privacy`

**Do you or your third-party partners collect data from this app?** Yes.

Apple counts data as "collected" when it leaves the phone and the developer (or a partner) can
access it longer than needed to serve the request in real time. Photos, videos and files go to
the owner's own device. Away from home they pass through the bridge's relay, which handles them
in memory only and stores none of it, so they are **not** collected. Neither is a photo's
location, which travels with the photo to the owner's device. The map in a photo's details comes
from Apple Maps, Apple's own service.

## Data types to declare

| Data type (App Store Connect) | iOS | macOS | Purpose | Linked to the user | Tracking |
|---|---|---|---|---|---|
| Contact Info > **Name** | yes | yes | App Functionality | Yes | No |
| Contact Info > **Email Address** | yes | yes | App Functionality | Yes | No |
| Identifiers > **Device ID** (the APNs push token) | yes | no | App Functionality | Yes | No |
| Diagnostics > **Other Diagnostic Data** (logs the owner sends) | yes | no | App Functionality | Yes | No |

Notes for each:

- **Name, email:** only when the owner creates a bridge account, during device setup or with
  Sign in with Apple/Google. The country of residence goes with them; App Store Connect has no
  type for it, so it is covered by the account declaration and the privacy notice.
- **Device ID:** the APNs token is stored by the bridge for the owner's device, so the device can
  notify the phone (a friend posted, the device went offline).
- **Diagnostics:** Settings > Logs > "Send to us" emails the device's recent logs and the
  account's email to info@off-the.cloud. It is sent only when the owner presses the button.
- Everything is "App Functionality". Account management and support count as functionality in
  Apple's categories. Nothing is used for analytics, advertising, personalisation or tracking.

## Not collected

Photos and videos, files, location, contacts, browsing and search history (searches run on the
owner's device), purchases, usage data, crash data (no crash reporting SDK), health, financial
info, sensitive info and audio. Face recognition runs on the owner's device and its results stay
there.

## Tracking

No. There is no advertising, no analytics or third-party SDKs, and no data is combined with other
companies' data. `NSPrivacyTracking` is false and there are no tracking domains.
