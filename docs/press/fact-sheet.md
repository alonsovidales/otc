# Off The Cloud — fact sheet

**Your family's photos, at home.** Off The Cloud (OTC) turns a Raspberry Pi 5 into a private home
server for a family's photos, videos and files, the job Google Photos, iCloud or Dropbox does
today, with an optional friends-only social feed between people's own devices. It is free,
open-source software (GNU AGPL-3.0), written by Alonso Vidales since August 2025.

- Website: https://off-the.cloud
- Live demo, no sign-up: https://demo.off-the.cloud ("Try the demo")
- Source code: https://github.com/alonsovidales/otc
- Contact: Alonso Vidales, avidales@off-the.cloud

## What it does

- **Backs up phones in full resolution, in the background.** Android app on Google Play; the
  iPhone app is in App Store review. Mac (Mac App Store), Windows and Linux apps keep chosen
  folders backed up or in sync. Everything else happens in a web app the device serves itself.
- **Finds any photo.** Image tagging ("beach", "dog", "birthday cake") and, if the owner turns it
  on, face recognition run on the Raspberry Pi itself; no photo is sent anywhere to be analysed.
  Place names come from the photos' GPS, looked up on the device. Photos are grouped by person,
  by date and in collections.
- **Keeps files private and safe.** Files are encrypted at rest with a key only the owner's
  password unlocks; the password itself is never stored. Two USB drives (pendrives, SSDs, hard
  disks or microSD cards in USB readers) are mirrored as RAID 1, and two optional status lights
  wired to the Pi's GPIO pins show each drive's health: green, red when one fails, flashing while
  a replacement fills. One drive can be rotated out as an off-site copy.
- **Shares without a platform.** An optional feed for posts with accepted friends only, device to
  device: no ads, no algorithm, no strangers. Collections can also be shared as a link.
- **For a whole family.** Each member gets their own encrypted account on the same device.
- **Seven languages:** English, Spanish, French, German, Italian, Portuguese and Dutch.

## How it runs

- **Hardware:** a Raspberry Pi 5 (8 GB; 4 GB with the AI features off), a microSD card for the
  system and two USB drives for storage, mirrored. Optional: two LEDs and resistors. The Pi and two
  512 GB USB sticks cost about €310 (October 2026), roughly €340 with the power supply, cooler and
  system card. The install script also runs on any 64-bit Debian or Ubuntu machine (an old PC, a
  mini PC, an ARM board).
- **Setup, no terminal:** the Mac, Windows or Linux app downloads the ready-made Raspberry Pi
  image and checks its signature. It writes the card itself on Windows and Linux; on a Mac it
  hands over to Raspberry Pi Imager. The setup then finishes over Bluetooth, from the computer or
  from the phone app: Wi-Fi, a name and a password for the device, the drives. It takes about
  20 minutes.
- **Away from home:** an optional relay, "the bridge", gives each device an address such as
  `casa.off-the.cloud`. It is free for the first two years, then €9.99 + VAT a year for up to five
  addresses. The bridge is open source too and can be self-hosted; Tailscale Funnel and a VPN also
  work. At home, the apps talk to the device directly.
- **Updates:** signed releases, installed from Settings.

## Status (October 2026)

- First device image: September 2026. Android app on Google Play and Mac app on the Mac App Store
  since October 2026, in most countries. iPhone app in App Store review.
- Everything, the device, the apps and the bridge, is AGPL-3.0 on GitHub.

## Images

See the [press kit](README.md#images): screenshots of the public demo, whose photos are
AI-generated pictures of a made-up family and can be published freely, and a photo of the device.
