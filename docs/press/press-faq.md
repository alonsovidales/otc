# Questions editors and commenters will ask

Short, factual answers to keep at hand when an outlet replies, and for comment threads (Hacker
News, Reddit, Hackaday). Lines marked [CONFIRM] are yours to check or rewrite in your own words.

**Why not Immich (or PhotoPrism, or Nextcloud)?**
Immich is excellent if you're happy running Docker on a server and sorting out remote access
yourself. Off The Cloud is a whole appliance instead:
- a signed Raspberry Pi image, set up from a phone or computer over Bluetooth, no terminal;
- RAID 1 storage whose state you can see on two status lights;
- files encrypted at rest with a key only your password unlocks;
- remote access through a relay with no port forwarding;
- one box for photos and for files, with sync apps for Mac, Windows and Linux;
- a friends-only feed between people's own devices.

It is for someone who wants Google Photos without Google, not another service to run.

**Aren't microSD cards going to wear out?**
That's why there are two, mirrored. When a card fails, the device keeps going on the other one,
sends an alert naming the USB port to replace, and turns that card's light red. A new card is
filled by itself, and the light flashes until it's done. The system lives on its own card,
separate from your files. Two USB SSDs or hard disks work just as well as the cards; cards keep
the build small and silent. [CONFIRM: if you have endurance figures for the cards you use, add
them.]

**Is it really private if a relay is involved?**
At home, the apps talk to the device directly. Away from home, the bridge passes your apps'
connections through to your device. It stores nothing of yours, and your files are encrypted at
rest on the device. Relayed traffic is encrypted between your app and the bridge, and between the
bridge and your device, but the bridge itself could read it: you are trusting whoever runs it.
That is why the bridge is open source and can be run on your own server and domain; Tailscale
Funnel and a VPN work too.

**What does it cost?**
The software is free and open source (AGPL-3.0): the device, every app and the bridge. You buy
the hardware. The hosted bridge is free for two years, then €9.99 + VAT a year for up to five
addresses, and it's optional. [CONFIRM: a current parts price. The Raspberry Pi 5 8 GB went up to
about $175 in April 2026, so the "about €250 for 1 TB" on the site and in the README needs
updating first.]

**What if the project stops?**
Your files never leave your house, and the code is open. You can download any folder as a ZIP,
pull folders to a computer with the sync apps, and restore a whole device onto a new Raspberry Pi
from one of its disks ("Recover this device").

**How is it built?** [CONFIRM: answer as you see fit; selfh.st's form and Lemmy's rules ask
directly.] Suggested: "I design and direct it, and much of the code was written with an AI coding
assistant (Claude), then reviewed and tested before every release. Releases are signed, and all
the code is public."

**Who is behind it?**
Alonso Vidales, a solo developer. [CONFIRM: a sentence about yourself and where you're based.]
