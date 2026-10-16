# Hackster project page (draft)

Publish it from your Hackster account 1–2 weeks before the launch, so moderation is done, and
mirror it on Hackaday.io. Every other pitch then links here. The technical sections below can go
in as they are. Write "Why I built it" yourself: it's the part editors quote.

**Name:** Off The Cloud: a Raspberry Pi 5 that replaces Google Photos
**Elevator pitch:** A family photo server on a Pi 5: full-resolution phone backup, on-device AI
search, encrypted RAID 1 storage on two microSD cards, and GPIO status lights.
**Cover image:** the real device photo, full resolution.
**Categories:** Raspberry Pi, Storage, Machine Learning, Home automation, Privacy

## Things (hardware)

| Qty | Part | Notes |
|---|---|---|
| 1 | Raspberry Pi 5, 8 GB | 4 GB works with Image Tagging and Face Recognition off |
| 1 | Active cooler | [CONFIRM: the one in the photo looks like a Geekworm Armor Lite V5] |
| 1 | Official Raspberry Pi 27 W USB-C power supply | a weaker one can brown out during photo analysis |
| 1 | microSD card, 8 GB or more | the system |
| 2 | microSD cards of the same size, e.g. 1 TB | storage, mirrored; two USB SSDs or hard disks work too |
| 2 | USB 3 microSD card readers | in the Pi's two blue USB ports [CONFIRM: model] |
| 2 | Red LEDs and 2 green LEDs (or 2 two-colour, common cathode) | optional status lights |
| 4 | 330 Ω resistors | one per LED colour |
| 1 | Case | [CONFIRM: the purple case's design, and whether you share the STL] |

**Software:** Off The Cloud (AGPL-3.0) https://github.com/alonsovidales/otc, Raspberry Pi
Imager (on a Mac), and the Off The Cloud app for Mac, Windows or Linux.

## Why I built it

[YOURS: a few paragraphs in your own words. Who it's for, what was wrong with the cloud, and
what you wanted the box to do.]

## What it does

- Phones back up every photo and video in full resolution, in the background: the Android app is
  on Google Play and the iPhone app is in App Store review. Mac, Windows and Linux apps back up or
  sync chosen folders.
- Photos are tagged on the Pi itself ("beach", "dog", "birthday cake"), and face recognition is
  optional. You can search by thing, by person, by place (from the photos' GPS) and by date. No
  photo leaves the box to be analysed.
- Files are encrypted at rest: AES-GCM, under a key unwrapped from your password with Argon2id.
  The password is never stored, so there's no reset and no back door.
- The two storage cards are mirrored as RAID 1. If one fails, the device carries on, tells you
  which USB port to replace, and refills the new card by itself.
- An optional feed shares posts with accepted friends only, device to device, with no ads or
  algorithm. Collections can be shared as a link.

## Wiring the status lights

Each light is a red LED and a green LED, each colour through a 330 Ω resistor. Wire them with the
Pi switched off.

| Light | Colour | Pi pin |
|---|---|---|
| 1 | red | pin 11 (GPIO17) |
| 1 | green | pin 13 (GPIO27) |
| 1 | both short legs | pin 9 (GND) |
| 2 | red | pin 15 (GPIO22) |
| 2 | green | pin 16 (GPIO23) |
| 2 | both short legs | pin 14 (GND) |

What they say:
- both green: both disks are fine and hold the same files;
- one red: that disk failed or was taken out, so replace it soon;
- one flashing red: a new disk is being filled;
- both red: no mirror is running;
- both off: the device is off or still starting.

## Setting it up (about 20 minutes, no terminal)

1. Install the Off The Cloud app on a Mac, Windows or Linux computer and choose **Set Up a New
   Device…**.
2. The app downloads the image and checks its signature. It writes the card itself on Windows and
   Linux; on a Mac it opens Raspberry Pi Imager and tells you what to pick.
3. Plug the two storage cards, in their readers, into the blue USB ports, insert the system card
   and power the Pi on. After a minute it announces itself over Bluetooth.
4. Finish over Bluetooth, from the Mac app or the phone app: your Wi-Fi, a name and password for
   the device, and how to use the disks.
5. Sign in. The phone app starts backing up your photos.

Away from home, the apps reach the device through an optional relay at `yourname.off-the.cloud`.
It's free for two years, then €9.99 + VAT a year, and it's open source so you can run your own.
Tailscale Funnel or a VPN also work.

## Under the hood

- **The device:** a Go server with a MariaDB database and a signed self-updater.
- **Storage:** mdadm RAID 1, watched by a small Python service that drives the LEDs.
- **AI:** image tags from the RAM++ model, faces from YuNet and SFace. Both run as ONNX models on
  the Pi 5's CPU, and analysis runs behind uploads, so backups stay fast.
- **Apps:** a React web app served by the device itself; native iOS (SwiftUI), Android (Kotlin
  and Compose) and macOS apps; a Go sync client for Windows and Linux.

## Try it

A live, read-only demo with a made-up family's photos, no sign-up: https://demo.off-the.cloud

Code: https://github.com/alonsovidales/otc
Site: https://off-the.cloud
