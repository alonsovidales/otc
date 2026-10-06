# App Store listing - Off The Cloud (iOS and macOS)

What goes into App Store Connect for the app (one record, `cloud.off-the.OffTheCloud`, with an
iOS and a macOS platform). The iOS text speaks to amateur photographers and families; the macOS
text to people who care about security and want a NAS of their own. Keep claims to what the
apps do: the relay terminates TLS, so never say it "cannot read" traffic, and the bridge does
have accounts. The Play listing (`docs/play-store/README.md`) follows the iOS one.


**Availability** (set 2026-10-06, the same Europe as Play; the bridge runs in France): the EU 27,
Iceland, Norway, the United Kingdom, Switzerland, Albania, Bosnia and Herzegovina, Kosovo,
Montenegro, North Macedonia, Serbia and Ukraine - 38 in all (Apple has no Liechtenstein; Play
has no Montenegro or Kosovo). Both platforms release automatically once approved. The EU trader
status (Digital Services Act) is declared under Business.

## iOS

**Promotional text** (170):

> Every photo you take, kept in full quality on your own device at home. Find any shot in seconds and share albums only with the people you choose.

**Keywords** (100):

> photo backup,photography,family photos,raw,albums,private,share photos,home server,nas,encrypted

**Description**:

```
Off The Cloud is a home for your photos. Every shot you take on your iPhone is backed up in full quality to a small device in your own home, instead of a company's cloud. No storage plan to outgrow, no compression, no one else looking through your family's pictures.

MADE FOR PEOPLE WHO TAKE A LOT OF PHOTOS
• Every photo and video backed up automatically, in the background, as the original
• Your camera's files too, RAW included, in folders that keep earlier versions
• Find any picture in seconds: search by what is in it ("beach", "dog", "sunset"), where it was taken, or who is in it
• Face recognition is off until you turn it on, and runs only on your own device
• See each photo's camera details, and where it was taken on a map
• Albums, and a date scrubber to move through years of photos

SHARE WITH THE PEOPLE YOU CHOOSE
• Share an album with grandparents and friends as a link that expires. They open it in their browser, with no account or app, and can download the originals
• A private feed for family and friends: photos, videos, likes and comments, kept on your devices and theirs, never on ours
• No ads, no ranking, no strangers: just the people you added, in the order they posted

FAST AT HOME
At home the app talks to your device directly over your own Wi-Fi, so a whole holiday's photos and videos back up quickly instead of crawling over the internet.

YOUR PHOTOS STAY YOURS
Everything is stored encrypted on your device and mirrored on two disks, so one disk failing loses nothing. Away from home the app reaches your device through our relay over an encrypted connection, and the relay keeps none of your photos. Prefer that nothing passes through us? Use it at home only, or with a relay of your own.

WHAT YOU NEED
This app connects to an Off The Cloud device: a Raspberry Pi with two disks running our open-source software. You can set up a new one over Bluetooth, straight from the app. Everything you need is at off-the.cloud.

Off The Cloud is open source: the device software and the apps are on GitHub.
```

**What's New** (1.0 has none: first version).

## macOS

**Promotional text** (170):

> Your own NAS, not someone else's cloud. Keep your Mac's folders backed up and in sync with a device you own, encrypted at rest, on open-source software.

**Keywords** (100):

> nas,self-hosted,backup,sync,encrypted,privacy,raspberry pi,home server,open source,raid,secure

**Description**:

```
Off The Cloud turns a small device in your home into your own NAS, and this app keeps your Mac's folders backed up and in sync with it. Your files live on hardware you own, encrypted at rest, running open-source software you can read. No cloud account holding your data, no subscription for storage.

BUILT FOR PEOPLE WHO CARE WHERE THEIR DATA LIVES
• Files are stored encrypted on your device (AES-GCM) and mirrored on two disks (RAID 1)
• Your device password stays in the macOS Keychain and never crosses the network in the clear
• At home the app connects to your device directly over your own network, and accepts only your device's own pinned certificate
• Away from home it goes through our relay over TLS. The relay stores none of your files, and you can run your own relay instead
• Device updates are signed, and your device checks the signature before installing anything
• Open source (AGPL): the device software and the apps are on GitHub, so you can check what they do

THREE WAYS TO SYNC A FOLDER
• Backup: new and changed files go up, nothing is ever deleted on the device, and earlier versions are kept
• Two-way, starting from your Mac or from the device: the folder stays the same on both sides, across several computers
• A guard against mass deletion: if a large part of a folder disappears at once, the other side keeps its copy
• Conflicts keep both versions: the losing edit is saved next to the winning one
• File dates are kept on both sides

A QUIET MENU BAR APP
• Sync progress and the health of your device's mirrored disks at a glance, with storage, CPU and memory
• Starts at login
• Set up a new device from your Mac: it downloads the signed image, checks it, opens it in Raspberry Pi Imager, and finishes the setup over Bluetooth

WHAT YOU NEED
An Off The Cloud device: a Raspberry Pi with two disks running the open-source Off The Cloud software. Everything you need to build one is at off-the.cloud. The same device also backs up your iPhone's photos with the Off The Cloud iPhone app.
```

**What's New in 2.0**:

```
Faster at home: the app now connects to your device directly over your home network, with a pinned certificate, and goes through the relay everywhere else. Plus reliability and security fixes.
```
