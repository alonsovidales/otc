# App Review notes - Off The Cloud for macOS (menu bar sync)

What to paste into App Store Connect > App Review Information > Notes for the macOS app
(`cloud.off-the.OffTheCloud`), one section per question Apple asks. Item 1 (the screen
recording) is made by hand on a Mac running the current macOS; it starts by launching the app,
opens the menu bar item, enters the device name and password in Settings, adds a local folder,
shows it reach "Watching" with a file appearing on the device's web app, then adds a remote
folder and shows the file come down. There is no registration, login-account or deletion flow in
this app (see item 2), no user-generated content to report or block, and nothing paid.

## 2. Purpose and audience

Off The Cloud is a personal, self-hosted alternative to cloud storage. A user owns a small
device at home (a Raspberry Pi with two mirrored disks running our open-source server) that holds
their photos and files; nothing is stored on our servers. This macOS app is the desktop
companion for that device: a menu bar utility that keeps folders on the Mac in sync with the
device, like a cloud-drive client but pointed at hardware the user owns.

It solves one problem: keeping a computer's folders backed up and available without handing the
files to a third party. Two directions are supported - a local folder mirrored up to the device
(changes and deletions propagate), and a device folder kept in two-way sync with a local one, so
several computers converge through the device. The menu bar icon also shows the health of the
device's mirrored disks. The audience is Off The Cloud device owners; the app has no purpose
without one.

## 3. Setup and access

The app needs an Off The Cloud device to talk to. For review, use our demo device:

- Device name: `<demo device name>` (this becomes `<name>.off-the.cloud` in the app)
- Password: `<demo device password>`

Steps:
1. Launch the app. It appears in the menu bar (a small rack icon); click it.
2. Click the gear, type the device name above under "Device name" and the password under
   "Password", then press Connect. The status turns "Connected" within a few seconds.
3. "Add Folder" > "Local Folder…", pick any folder on the Mac. It shows a progress bar while its
   files upload, then "Watching". Files you add or delete in that folder follow within seconds.
   They are visible on the device at `https://<name>.off-the.cloud` (same password) under
   Files > `/mac/<your Mac's name>/…`.
4. "Add Folder" > "Remote Folder…", pick a folder on the device and a local destination. It
   downloads and stays in two-way sync ("Synced").
5. The minus button next to a folder stops syncing it; nothing is deleted on either side.
6. "Start at login" (in the gear panel) is on by default.

There is no account to create in this app: the password is the owner's device password, set
when the device itself was installed. The app stores it only in the macOS Keychain.

## 4. External services

- The Off The Cloud bridge (`off-the.cloud`), operated by us. Devices sit behind home routers,
  so the app connects to the bridge, which relays the connection to the user's own device. The
  bridge cannot read the user's password (it is encrypted end to end with a key from the device)
  and stores no user files or content. A user with their own bridge, a LAN address or Tailscale
  can point the app at that address instead.
- Apple Keychain, for the password. Nothing else: no authentication provider, payment
  processor, analytics, advertising or AI service. The app makes no other network connection.

## 5. Regional differences

None. The app has one feature set and one language (English) everywhere; there is no regional
content, pricing or gating. The bridge is reachable worldwide.

## 6. Regulated industry / third-party material

Not applicable. The app is a file-sync utility for the user's own device and contains no
third-party licensed content or regulated services. The software is open source (AGPL-3.0) at
https://github.com/alonsovidales/otc.
