# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

OTC ("Off The Cloud") is a self-hosted NAS + ethical social network. A Go server (`otc`) runs on a
Raspberry Pi with a RAID1 disk pair, storing photos/videos/documents and serving a React web app,
iOS/macOS Swift apps, and a Windows/macOS sync client. Devices are not directly reachable from the
internet, so a separate relay service (`bridge/`) proxies WebSocket connections between a device and
its friends'/owner's clients using a subdomain like `<device>.off-the.cloud`.

Two independent Go modules share one `go.mod`/proto definition:
- **Device (root)**: `bin/otc.go` — the NAS + social server that runs on the Pi.
- **Bridge** (`bridge/`): `bridge/bin/otc_bridge.go` — the public relay server (its own `dao`,
  `websocket`, `api`, deployed separately to a cloud VM, not the Pi).

## Build & deploy commands

All builds/deploys are driven by `make`, and most targets `ssh`/`scp` straight to a live device —
they are not local-only build steps. `makefile` targets `TARGET` (edit this to the device's
hostname/IP, e.g. `pit.otc`) over SSH as user `otc`.

```
make all      # clean + regenerate protobuf + sync source + build web + build & restart device binary
make otc      # cross-compile the device binary for linux/arm64 (see toolchain note below) and scp it to TARGET
make pi       # build ON the device itself over SSH (stop otc, go build, restart otc)
make web      # npm run build (web/) then copy dist/ into ios app and scp to TARGET (not the bridge - see issue #95)
make pb       # regenerate proto/generated/*.go, web/src/proto/*.ts, and app/*/OffTheCloud/*.pb.swift from proto/messages.proto
make sync     # rsync the whole repo to the device (excludes handled by rsync flags)
make clean    # remove generated protobuf, the otc binary, and ios web-dist
make image    # issue #38: build the flashable Pi image ON TARGET (scripts/build_image.sh) from HEAD's scripts/ and copy it to dist/
make image-publish  # check dist/off-the-cloud-rpi-lite-arm64.img.xz against its .sha256, sign that, upload them to the rolling "image" GitHub release and check what was published
```

`Makefile.pi` (README's Option 2) sets up a device on a plain Raspberry Pi OS from another
computer: its `bootstrap` shows the two disks, asks before it lets them be wiped (an OTC mirror on
them is always kept), and runs `verified-install.sh` on the Pi over SSH - the same install as the
one-line script, so the device updates itself and gets the system's security updates. It logs in
with an account of the owner's (`PI_USER`, never `otc`: the installer makes that the service's
account, with no login) and needs GNU make 3.82 or newer (`.ONESHELL`; macOS ships 3.81, so
`brew install make` and `gmake`). Its own step-by-step bring-up (unpinned downloads, a config
every account could read, no update runner, release key or version file - a device that never
updated) is gone.

Without a Pi at hand the image builds in the Lima VM just as well (arm64 Ubuntu with losetup and
chroot): from a clean checkout, `limactl shell otc -- mkdir -p /tmp/otcrepo` and `git archive HEAD
scripts | limactl shell otc -- tar -xf - -C /tmp/otcrepo`, then in the VM `sudo
WORK=$HOME/image-build SRC_REPO=/tmp/otcrepo bash /tmp/otcrepo/scripts/build_image.sh` - WORK on
the VM's disk, not under `/tmp`, which is a RAM disk too small for the decompressed image - and
`limactl copy` the `.img.xz` and its `.sha256` into `dist/` for `make image-publish`. The image is
built from a committed tree only: every device made from it runs its scripts as root. `make image`
refuses to start while `scripts/` has changes or untracked files, and ships a `git archive` of
HEAD's `scripts/` to `TARGET:/home/otc/image-src` (never the working copy, which `make sync`
copies); `make image-publish` checks the image against its `.sha256` before signing that (a new
image beside an old checksum was signed as it was, and failed every desktop app's check), and
downloads the three published files again to compare them. `build_image.sh` keeps the base's own
`/etc/resolv.conf` (a file or a symlink, moved, never written through) aside while the chroot uses
the build machine's for apt, and puts it back at clean-up, a failed build's too: the published
image carried the builder's DNS servers and search domains. The base is
one dated Raspberry Pi OS Lite release pinned with the SHA-256 Raspberry Pi publishes beside it
(`BASE_URL`/`BASE_SHA256` in build_image.sh, checked before anything is unpacked; a cached base from
another pin is fetched again): a newer base is a deliberate edit there.

The image (issue #38) is stock Raspberry Pi OS Lite plus `scripts/setup_wizard.py` (a root,
stdlib-only web wizard on port 80: WiFi, device name reserved on the bridge via its public
`/api/name-available` + `/api/claim`, disks, then runs `install.sh` and shows its `[n/10]` steps as
a progress bar) and `scripts/network_setup.py` (root: unblocks the radio, makes the wizard's Wi-Fi
join, keeps any hotspot down). Since the 2026-09-29 security advisory setup is over Bluetooth only
(#137, below): the wizard listens on 127.0.0.1 (`OTC_SETUP_BIND`) and is reached only through
setup_ble.py, and the open "Off The Cloud" hotspot (a virtual `uap0` beside `wlan0`; one radio
means one channel, so a join over it was pinned to 2.4 GHz) is raised only with
`OTC_ALLOW_HOTSPOT=1`, which nothing sets - its code (captive DNS, `ap_allowed_on`, the band pin,
the wizard's hotspot texts) stays for that switch alone. network_setup.py takes down a hotspot left
over from an older image on every poll before setup and rechecks once a minute after it
(`ap_recheck_s`), lifting the 2.4 GHz pin once (`lift_band_limit`: only an `OTC-WiFi` still pinned
to a band is changed and brought up again, and the marker that it was done is root's,
`/var/lib/otc-network-setup/wifi-band-lifted` - one in the otc user's `/var/lib/otc`, which that user
could remove, had root re-activate the Wi-Fi every minute); a Bluetooth join uses any band
(`any_band` in the join request). `python3 scripts/network_setup.py --selftest` tests the lift with
nmcli stood in for. network_setup.py makes its join only before setup - never once
root's `/etc/otc/.install-complete` exists (`setup_done()`'s own marker is in the otc user's
`/var/lib/otc`) - opens the request with O_NOFOLLOW (a regular file of at most 4 KiB), and
removes anything else found under that name (a directory too) without reading it. Release 143 puts
it on devices imaged before 2026-09-29 (whose copy raised the open hotspot whenever the network was
lost) and deletes the hotspot's NetworkManager profile (an access point named `OTC-Setup` or with that
SSID); `update.sh` reinstalls it wherever `/usr/local/bin/network_setup.py` exists. After setup the
Wi-Fi changes only through Settings › Wi-Fi (`network`, below) or the console. The wizard changes state only through `update_state()` under
`_state_lock` (never held across bridge calls) and writes files with `atomic_write`. `raid_watch.py`
runs no mdadm query or repair unless md0 is in `/proc/mdstat`, and backs off failed adds.
Fallback if the phone still loses the page: the device reports its LAN address to the bridge under
a one-time token (`POST /api/setup-beacon`, bridge DB `setup_beacons`, 10-minute expiry) and the
page polls `GET /api/setup-lookup`. Recovery safety: an array assembled without an mdadm.conf (the wizard's own check, a fresh
image) comes up as `/dev/md127`, not `md0`; `install.sh` adopts any active array on its disks and
reassembles it as `md0`, never wipes a disk that still has a RAID superblock, and with
`OTC_RECOVERY=1` (the wizard's Recover) refuses to build a fresh array at all - the wizard only
sets `OTC_RAID_CONFIRM_WIPE` on a fresh install. A mirror down to one disk is recovered too: the
wizard names that disk as both `OTC_DISK1` and `OTC_DISK2`, which only `OTC_RECOVERY=1` accepts,
and the adopted array is reassembled with `--run` (without it a degraded array is only gathered,
inactive). The storage's fstab line has `nofail,x-systemd.device-timeout=3min` (install.sh, release
143 for older devices: only the md0 line's options change) and `otc.service`/`mariadb.service` get
`RequiresMountsFor=/mnt/storage` drop-ins (`otc-storage.conf`): a disk missing or slow at boot no
longer stops the boot in emergency mode with no network or SSH. The device comes up without its
storage; the service and MariaDB don't start (never on the empty directory under the mount point).
Issue #207 (release 166): a minute after the storage went missing the device tells the apps why -
`storage_unavailable` (the `nostorage` package below) - and starts again by itself once it is
back, with no restart. Without the mount, `raid_watch.py`'s `StorageGuard` starts
`otc-degraded.service` (`otc <env> degraded`: no database, the mount point read-only in its
namespace, `Conflicts=otc.service`, no [Install]) 60 s after it first saw it missing - since boot
when the boot is recent; `raid-watch.service` is no longer `After=multi-user.target`, which waits
for otc.service's start job, three minutes (the device timeout) when the disks are missing. Once the
storage's device is there (fstab's source: an md array only when active in `/proc/mdstat`, else a
block device), it runs `systemctl start --no-block mnt-storage.mount` (30 s doubling to 10 min while
it fails; at once when the device reappears): systemd never mounts a nofail line whose device came
after its timeout (seen in the Lima VM: `dev-md0.device`'s job times out, the mount, MariaDB and
otc are never started again). Mounted after the companion ran (or after 60 s), it starts
`mariadb.service otc.service`, which stops the companion. With the storage mounted but MariaDB not
answering (InnoDB failing, a read-only datadir), otc.service runs and its own wait does the same
in the process (`nostorage.Wait`). The LEDs show the storage missing as both red in turn, a second
each (`turns_red`). Tested on 2026-11-03 in a throwaway Lima VM (Debian 13, two 3 GiB virtual
disks as md0, installed by install.sh, `bridge-addr` pointed at a fake bridge in the VM with its
certificate trusted), each answer checked locally and through that bridge (GetPubKey in the
request's language, the page served, `/check_healty` 503): MariaDB's datadir made unreadable
(otc.service up, its own degraded mode at 61 s, the full start in the same process within 5 s of
MariaDB starting again, Alert "for 1 minute"); `raid1` blocked at boot (`install raid1 /bin/false`
in the initramfs, md0 inactive: the companion at 60 s of uptime, otc.service's waiting start job
cancelled, the mount job timing out at 181 s; the mirror then assembled by hand - systemd left it
unmounted with `dev-md0.device` active - was mounted and otc started within 4 s of raid-watch
looking, Alert "for 5 minutes" from the boot); both disks detached (the companion at 61 s, the
record noted every minute), then attached again with a restart (Alert "for 4 minutes": until it
was last seen missing); a hand `systemctl stop mnt-storage.mount` mounted again in 2 s. The empty
directory under the mount point kept its time throughout, and the companion sees it read-only.
Release 166's script, run twice on the VM with the unit removed and the old ordering put back,
wrote both. Both disks pulled while the device runs (the rest of #207): md never fails a raid1's
last disk - it calls the array broken (`/proc/mdstat` and `array_state` say `broken`; every write
fails) and keeps the gone disk as an in-sync member - so md0 and its mount stayed, MariaDB aborted
and otc answered every request with an error ("error checking device state") until a restart.
`StorageGuard` now also judges the storage while it is mounted (`storage_gone`, every 2 s, from
sysfs and mountinfo alone, nothing read from the disks): its device gone (`/sys/dev/block/<maj:min>`:
the single disk pulled), an md array `broken` or running with no member in sync, not faulty, whose
disk is there and not offline (`md_dead`; a mirror down to one working disk runs on, as before),
its disk offline (`device/state`), or - its device then read once, O_DIRECT in a child given up
after 10 s (`probe_read`, at most a minute apart), and gone only if that fails - a filesystem
that turned itself read-only or whose ext4 error count or time moved since the mount was first
seen (`ext4_errors`: from Linux 6.9 ext4's emergency read-only after an I/O error doesn't show in
mountinfo). Only a filesystem on a block device is judged, and only when fstab's source names one it
can mount back (`source_names_device`: UUID=, LABEL=, PARTUUID=, PARTLABEL= or a /dev path): btrfs,
ZFS, NFS, SMB and FUSE mounts show an anonymous `0:NN` with nothing under `/sys/dev/block`, and a
storage mounted beforehand that way (install.sh's "already mounted") was taken for gone at the first
look - a btrfs one stopped and mounted again every few seconds, an NFS one never mounted again. It
then takes it down (`_begin_down`/`_down_step`): `systemctl stop --no-block
mariadb.service otc.service` (SIGKILL after 30 s, the mount goes 10 s after that regardless:
nothing they write reaches a disk, and InnoDB recovers from its log as after a power cut),
`umount`, then `umount --lazy` when busy (each through `run_bounded`, which gives up on a command
stuck on a dead disk so the loop and the LEDs go on), and, once nothing is mounted from it,
`mdadm --stop`s the dead array (10 s doubling to 5 min while a detached filesystem still holds
it), so the disks plugged in again are assembled afresh by udev's `mdadm --incremental` from their
own superblocks: the newer disk wins, one that dropped out first is kicked as non-fresh and re-added
as a former member, copied from the newer. Disks already back when it is stopped send no further
event (a USB brownout drops both for a second; they come back while md still holds the broken array,
and their incremental add fails against it), so while fstab's array is not in `/proc/mdstat` at all
the disks nothing holds that carry its superblock (its UUID from mdadm.conf, read with a bounded
`mdadm --examine --export`) are handed to udev again (`_assemble_again`: `udevadm trigger
--action=add`, the same incremental assembly and last-resort start as a plug-in; not `change`, which
trixie's md rule, mdadm 4.4, skips for a bare disk - bookworm's 4.2 ran it on both) - at once after
the guard's own stop, otherwise once it has been apart 30 s (udev's own assembly at boot or a plug-in
is far quicker), then 30 s doubling to 10 min. Which array that is goes by fstab's source
(`_md_name`, used by `_array_of` too): `/dev/mdN` as install.sh writes it, or what a `UUID=` line
(raid_watch's own first-sign-in setup writes one), `LABEL=` or udev link led to - a stopped array
takes its node and its filesystem's by-uuid link with it, so the guard remembers the name from the
last time the source resolved (`md_seen`), and one started while the array was apart takes
CONFIG's `raid_dev` when mdadm.conf has its ARRAY line. Checked on 2026-11-04 in a throwaway Debian
13 VM (mdadm 4.4, two loop disks as md0, the guard on the real sysfs, udev and systemd): md0 failed
to broken, taken down, stopped and assembled again by the add trigger and mounted 4 s later, for a
`UUID=` and a `/dev/md0` line, and by a guard started while it was apart; with `change` it stayed
apart. A dead array is never mounted (`source_present`) nor
added to (`guard.dead_array` gates the repair), and the companion starts as soon as the storage is
unmounted (grace 0; the outage, and the Alert's minutes, begin at the detection), as it does when a
storage seen mounted goes with its device (the single disk, which systemd unmounts itself,
stopping what needs it). The repair and the grow now wait until md0 has run 10 s
(`running_settled`, `REPAIR_SETTLE_S`): in the VM a disk plugged back in was judged while udev was
still gathering md0 (inactive) and the newer one was picked to be added - it failed only because
md held it. No release script: update.sh reinstalls `raid_watch.py`. Tested on 2026-11-03 in a new
throwaway Lima VM (`otc-raid207`: Debian 13, kernel 6.12, mdadm 4.4, two 3 GiB virtio disks as md0,
release 167 installed by `verified-install.sh` local-only, the new script put in place as update.sh
does, its repair let to take the VM's `vdX` disks for the test - a Pi's are `sdX`), 40 files
seeded and a test client uploading 6 MiB files in 1 MiB chunks with an upload under way at each
pull, a disk pulled by hot-removing its virtio PCI device (`echo 1 >
/sys/bus/pci/devices/<slot>/remove`: the disk deleted, udev's remove events and `mdadm -If`, every
I/O failing at once, as a USB unplug) and put back with `echo 1 > /sys/bus/pci/rescan`. Release 167
first: md0 broken and still mounted, ext4's journal aborted, MariaDB aborted, otc answering errors;
the new script put in place over that took it down in 3 s. Both pulled 60 ms apart: detected
within the look, otc stopped cleanly, MariaDB aborted on its own I/O errors while stopping,
unmounted and md0 stopped 2 s on, `storage_unavailable` (GetPubKey and the 503) 3.1 s after the
pull; back after 100 s: md0 assembled at once, mounted 1 s and MariaDB and otc started 3 s
later, the device answering at 6 s, Alert "for 2 minutes". Pulled 20 s apart with the uploads going on to the second disk alone: md0 assembled from
the newer one (events 124 against 27), the older added back 10 s after md0 started and copied from
the newer; then the other disk pulled with the page cache dropped, every file read back from that
copy alone. One disk out for 40 s: nothing stopped, the upload under way finished, re-added once
back. `raid1` blocked at boot: the companion at 61 s of uptime, no disk added to the inactive md0,
mounted 1 s and MariaDB and otc started 3 s after `mdadm --run`. Disks there but failing every
I/O (dm-error under a throwaway md207 and under a plain ext4, read through the installed module): md207 `broken`
taken as gone and one failing member not, the plain ext4's errors (errors_count 3, still `rw` in
mountinfo) with its device unreadable taken as gone within 5 s, a read-only mount on a disk that
answers not. A single storage disk (the VM installed again with `OTC_SKIP_RAID`, `vdb` made the
storage by the first sign-in's own `perform_pending_storage_setup`, its `sdX` check passed over):
release 167 already came back by itself - systemd unmounts the pulled disk and stops what needs
it - but told the apps only after 60 s; now `storage_unavailable` 1.2 s after the pull and the
device answering 4 s after the disk was back; with a process's working directory holding the
mount, systemd's unmount failed and the guard detached it (`umount --lazy`) in the same second,
the disk coming back as `vde`, mounted by its UUID. After each, every stored file (432 on the
mirror, 64 on the single disk) read back with its SHA-256 and `mariadb-check --all-databases` all
OK, and the empty directory under the mount point never changed. The LEDs (no GPIO in the VM) are
checked by the selftest only. A fourth: the Pi has no battery-backed clock, so an install that starts
before NTP syncs sees every Debian repository as "not live until <date>" and apt fails with
code 100 - `install.sh`'s `ensure_clock` waits for NTP and falls back to a verified HTTPS
Date header (forward only). Three Pi 5 pitfalls learnt the hard way: the image strips SSH host keys, so enabling ssh on an older card needs `ssh-keygen -A` first (newer images regenerate them from a ssh.service drop-in); NetworkManager's WiFi
switch ships off on stock Raspberry Pi OS (`nmcli radio wifi on`), and an idle `wlan0` reports
channel 34 (5170 MHz) - never copy a channel from an interface that isn't connected. The wizard also handles a re-imaged
device: it assembles any existing RAID1 array (`mdadm` is the one package baked into the image)
and, if it holds an OTC database, offers recovery - `install.sh` reassembles instead of wiping and
the identity in that database wins, so no name is asked; after installing it polls the bridge's
`/api/device-online` and only asks for a name if the recovered device never shows up. It contains
no otc code, so it only needs rebuilding when those scripts change. Issue #137: `scripts/setup_ble.py`
(unit `otc-setup-ble.service`, same lifetime as the wizard, needs the image's `python3-dbus` +
`python3-gi`) offers the *same* wizard over Bluetooth LE for the apps - one GATT service
(`0f7c5e70-…-0001`; write chunks of a JSON request in, notify chunks of a raw-DEFLATE JSON
answer out, MTU-3 per chunk) that forwards every request to the wizard on 127.0.0.1:80.
`BluetoothSetupView` (iOS `.swift` / Android `.kt`) shows the wizard's page in a web view: the
page itself comes through a scheme handler / `shouldInterceptRequest`, and a script injected
into `<head>` swaps `fetch()` for a call into the app (`WKScriptMessageHandlerWithReply` /
`@JavascriptInterface`), because neither web view hands POST bodies to an interceptor
reliably. The view watches `/api/state` for `install.phase == "online"` and then offers "Use
this device", which fills the onboarding form's endpoint (bridge name → wss, no bridge →
`ws://otc.local:8080/ws`). Any other domain the wizard reports - a bridge of one's own, or a
look-alike (`name.off-the.cloud.example.net`) from something nearby passing for the device - is
opened only after a question that shows it (`domainNeedsConfirm` in both apps,
`app.shell.setup_other_domain_title`/`_text`): the photos would go there. The pending setup
(below) is saved before any question, so Onboarding's Save & Continue asks the same before it
signs in to it (`setupAddressToConfirm`; not for an address typed over it). What that leaves
open: the setup has no pairing secret, and a lone advertiser is connected to at once, so
anything nearby advertising the setup service while the owner's device still boots is taken for
it - and when it reports an off-the.cloud name of its own (any device registered on the bridge,
the Docker image's too), "Open my device" signs in there unasked with the password its page
handed over, and the library goes there after the sync question. The domain question only stops
look-alikes; the checks against that are the address named on the "Open my device" step
(`app.shell.setup_ready_as`) and the page's "Blink its light". `python3 scripts/setup_ble.py --selftest` checks the framing and
forwarding without Bluetooth; for the phones, `scratchpad`'s `blesim.swift` (a CoreBluetooth
peripheral on the Mac forwarding to a dry-run wizard, e.g. the Lima VM's on port 8090) stands
in for a device - it needs Bluetooth permission for the terminal, which macOS prompts for. The
Bluetooth setup is open by design and only exists before the install completes.
The link recovers by itself: on iOS/macOS any state but poweredOn forgets the peripheral
(`forgetLink`) and a device already picked is rescanned for once Bluetooth is back; Android checks
every GATT step, drops and rescans on a failure, with a 30 s connect watchdog.
The owner password is chosen on the wizard's name step, and since both channels are readable by
anyone nearby it never travels in the clear: the wizard makes a one-off RSA-2048 key at start
(`SealKey`, stdlib Miller-Rabin), the page seals the password with RSA-OAEP-SHA-256 written in
plain JS (`sealWith` - no WebCrypto on a plain-HTTP page), the wizard opens it into a root-only
tmpfs file, and `install.sh` feeds it to `otc <env> init-owner-password` (as `otc`, before the
service first starts; `bin/ownerpassword.go` - a device that already has one keeps it) and
shreds the file. In the app the page also hands it to the app (`window.otcSetupPassword`,
memory only), so "Open my device" signs in without typing. Since the device drops its Bluetooth
setup service once installed, a phone locked through the install never sees the "online" state;
so the app also saves the domain (from `/api/state` once named) and that password as a *pending
setup* (Keychain / EncryptedSharedPreferences `setup_endpoint`/`setup_password`) as soon as it has
both, Onboarding fills its form from it, and `persist()` of a configured store (or Log Out)
clears it. The same step has an optional,
collapsed "Your profile and face recognition" section (issue #178): name, about text, a picture
cropped in the page to a circle (canvas pan/zoom, sent as a <=96 KB JPEG, base64) and a face
recognition checkbox, unticked. The wizard writes them to a root-only tmpfs JSON
(`OTC_SETUP_PROFILE_FILE`), and `install.sh` feeds it to `otc <env> init-profile` (`bin/otc.go`)
before the owner password - empty fields keep the defaults, and all of it can be changed later in
Settings. On Android the setup WebView needs its `WebChromeClient.onShowFileChooser` for the
picture field, and Android's web view can't decode HEIF/HEIC (what most phone cameras save): a
picture of that kind goes to the page as a JPEG copy of at most 2048 px a side (`ui/SetupPicture.kt`:
ImageDecoder, else MediaMetadataRetriever's primary image - the emulator's ImageDecoder refused a
tiled HEIC; in `cacheDir/tmp/setup/`, removed as the screen goes; debug-only
`SetupPicturePreviewActivity` runs a picture through it), and the page says
`wiz.profile.picture_unsupported` in place of its drag hint for any picture its browser can't open
(it said nothing and left the crop empty, 2026-11-05); WKWebView handles file inputs itself on iOS
and opens HEIC, but on macOS only through a
`WKUIDelegate` (`runOpenPanelWith`, an `NSOpenPanel` for images - without it "Choose File" does nothing). Several devices can be set up at once from several phones: each advertises
`OTC <id>` (`device_id()`: the last 4 hex digits of the Pi serial), the setup page shows that ID
with a "Blink its light" link (`/api/identify` flashes the ACT LED for 15 s), and the apps list
every device found in the first 2.5 s when there is more than one (strongest signal first),
connecting straight away when there is only one. A dropped link reconnects only to the device
picked. Leaving the setup screen sends `/__otc/release` (handled by setup_ble itself), which
frees the phone binding unless the install has started, so a device opened by mistake can
still be set up from another phone. The image has no SSH; its
console login is `otc-debug` / `off-the-cloud` (with sudo), set in `build_image.sh` and documented
in README.md under "Console access" - the only way into a device like Cala short of enabling SSH
from that console. System updates (release 161, critical; CVE-2026-98432, a Linux IPv4 TCP
use-after-free with IP options; owner, 2026-11-01): until then a device never updated its OS.
`otc_os_security` - the same function in install.sh and `updates/corrected/161.sh`
(`updater/os_security_test.go`, the whole function; corrected scripts under "Cutting a release"),
skipped in a container - installs unattended-upgrades and
nftables, enables daily runs (`20auto-upgrades`), allows the Raspberry Pi archive's main component
besides the distribution's own security origins, reboots at 04:00 when an update needs it
(`52otc-security-updates`; `OTC_SECURITY_REBOOT=0` or an existing "false" keeps it off), makes apt
wait 600 s for a lock, and loads `/etc/otc/ipv4-options.nft` at boot (`otc-ipv4-options.service`,
`WantedBy=sysinit.target`): its own table drops TCP with IPv4 options (`ip hdrlength != 5`; TCP only,
so IGMP's router-alert option still passes). Tested on the 2 GB Debian 13 VM: the rule loads at
boot, re-runs are idempotent. Release 161 as released (`set -e` around `apt-get install`) stopped
every later update for good on a device whose apt was unwell - an update has no terminal, so
dpkg's question about a changed configuration file failed, and so did a dpkg a power cut left
"interrupted", another apt holding the lock, stale lists. The function now asks nothing
(`DEBIAN_FRONTEND=noninteractive`, dpkg's `--force-confdef --force-confold`,
`DPkg::Lock::Timeout=600` on apt's command line), finishes what dpkg left (`dpkg --configure -a`),
tries a failed install once more with fresh lists, and sets up all it can before it reports a
failure (status 1) - the filter wherever nft is there already. install.sh stops on that failure (a
fresh install is run again); the corrected 161 never stops the update: it leaves root's
`/etc/otc/os-updates.pending`, every later update that installs a release runs it again until
everything is in place (and removes the mark), and the update ends with `updated_os_pending`
("Updated to version X, but the system's security updates could not be set up: the update to the
next version tries again" - an update with no release to install stops at "Already up to date"
and runs nothing, and Settings offers Update only when there is one) - tested with stand-ins for
apt, dpkg, systemctl and nft (`updater/release161_test.go`).
TCP keeps the window it learnt on a connection that sat idle (release 169: `otc_network_tuning`,
the same function in install.sh and `updates/169.sh`, `updater/release169_test.go`;
`net.ipv4.tcp_slow_start_after_idle = 0` in `/etc/sysctl.d/80-otc-network.conf`, set at once and
at every boot, not in a container): the bridge connections the device keeps open for the apps sit
idle between requests, and each 4 MiB piece of a video sent over one started again from slow
start, several round trips before full speed. A failure only warns. The servers got the same rule by hand in ufw's `before.rules`
(bridge1, bridge2, redis; a `u32` match) until Ubuntu's fixed kernel (6.8.0-149) ships.

The bridge has its own `bridge/makefile` (`make -C bridge bridge`) which builds
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0` and deploys to every cluster node (`NODES ?= bridge1
bridge2`, SSH aliases for `ubuntu@37.187.141.41` / `ubuntu@149.202.83.7`), one at a time so the
other keeps serving. Since issue #144 (2026-10-01) the bridge is a cluster - two KS-5 nodes behind
DNS round robin (`@`, `www`, `*` at a 60 s TTL), MySQL primary on bridge1 and replica on bridge2,
Redis on the old KS-B (51.83.103.72, `redis`), everything internal over WireGuard; see
`bridge/cluster/README.md`. The old server no longer runs the bridge. The Mac's scheduled
`bridge/cluster/servercheck.sh` also checks each node's served certificate and fingerprints
`authorized_keys2` and systemd units; a host gets 240 s (plus a 300 s watchdog on the Mac), a run
cut short skips the fingerprint comparison, and a new *kind* of fingerprint line (`unit:`,
`unit_link:`) joins the baseline without an alarm.
The same `make -C bridge bridge` then runs `fleet`: it builds `otc-fleet-agent`
(`bridge/fleetagent`) and installs it with its unit on all three hosts (`FLEET_HOSTS`) - the admin
panel's Fleet tab and its email alerts (`bridge/fleet`; README "Fleet tab and alerts", which has
the one-time Redis ACL user and MySQL monitoring user to create first).

**Toolchain requirements** (not present by default in a generic dev container):
- `go.mod` requires Go **1.25+**; the system `go` may be much older (check with `go version` before
  assuming `go build`/`go vet`/`go test` will work).
- The device binary needs **CGO** and links against **ONNX Runtime** for `images_tagger` (RAM++ image
  tagging model, via `github.com/yalue/onnxruntime_go`). Cross-compiling for arm64 (the `otc` make
  target) expects an aarch64 cross-compiler (`aarch64-unknown-linux-gnu-gcc`) and an ONNX Runtime
  aarch64 build unpacked at `~/ort-aarch64/onnxruntime-linux-aarch64-<version>/`. Building/running
  natively on the device instead avoids needing the cross toolchain (see `make pi`).
- The device binary also needs **CGO** + a real **OpenCV** install (headers/libs, found via
  `pkg-config`, not just a runtime `.so`) for `face_recognition` (issue #52's "People" search, via
  `gocv.io/x/gocv` - pinned to v0.40.0, matching OpenCV 4.10.x). `apt-get install libopencv-dev
  pkg-config` on Debian 13/Raspberry Pi OS; where apt's OpenCV is older than 4.10 (Debian 12, Ubuntu
  24.04, Linux Mint 22) `scripts/install.sh` builds 4.10.0 from source into `/usr/local` (pinned by
  hash, only gocv's modules); on macOS, `brew install opencv@4` (plain `brew install
  opencv` currently installs OpenCV 5, which gocv v0.40.0 doesn't build against) and add
  `$(brew --prefix opencv@4)/lib/pkgconfig` to `PKG_CONFIG_PATH` if `pkg-config --exists opencv4`
  doesn't already find it.
- `make pb` needs `npx protoc` with the Go, Go-gRPC, ts-proto, and Swift protoc plugins available
  (`go install google.golang.org/protobuf/cmd/protoc-gen-go@<go.mod version>`, `go install
  google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`, `brew install swift-protobuf`; ts-proto comes
  from `web/node_modules` after `npm ci --prefix web` - the pb target puts both on its PATH).

**Tests**: most device and bridge packages have `_test.go` files (`face_recognition` needs the
OpenCV install above). The MySQL ones (`bridge/accounts` `TestInactivityPassMySQL`, `bridge/dao`'s
`TestAccountPasswordMySQL`, `TestEmailTokensMySQL`, `TestEmailLinksSpentWithTheirWriteMySQL`,
`TestListAdminDevicesMySQL` under `ONLY_FULL_GROUP_BY`, `TestLanguageMySQL`, `TestLastOnlineMySQL`,
`TestNameHoldMySQL`, `TestHashedSecretsMySQL`, `TestUserAddressesMySQL`) skip unless `OTC_TEST_MYSQL_DSN`
points at the Lima VM's bridge schema (with `&time_zone=%27%2B00%3A00%27`, as the bridge's own
DSN); the ones about an optional column run against both schemas - the VM's `otc` database has
migrations 010 and 012, `otc_b1_nolang` neither (neither has 011's tables, so `TestGrowthMySQL`
fails there). `TestHashedSecretsMySQL` is meant for each step of the move to hashed secrets: a
schema from before migration 013 (`devices.secret` and `account_tokens` only), one with 013 run
(both forms), and one with 014 run or a fresh `db.sql` (the hashes only) - it says which it found,
and refuses a schema between the steps; `TestUserAddressesMySQL` runs with or without migration
015's columns, and says which it found. A scratch database of your own (`db.sql` without its first
lines, which create the `otc` user and database) and a user for it, both dropped afterwards, keep
the shared VM's alone. A
package var a test shortens must be restored only after the handlers reading it are done (a plain
`defer` raced under `-race`): bridge tests use `setVar` with a test bridge that waits for its
handlers; httptest's `Close` doesn't wait for hijacked websockets. `websocket/write_reply_test.go`'s
slow reader dials with gorilla's default buffers: with 16 KiB at both ends Linux loopback slows to
about 150 KB/s and a 64 KiB piece can miss the shortened `cReplyStall`, which no real peer would
see.

**Web app** (`web/`, Vite + React 19 + TypeScript). To *see* it on a device from a
terminal session, `scripts/dev/webshot.mjs` drives a headless Chrome over the DevTools protocol
(sign in, open a tab, hover/click one element, PNG per step) - through an SSH tunnel to loopback,
because macOS's local-network permission blocks a terminal-launched Chrome from LAN addresses and
Chrome doesn't resolve `.otc` names; see the header comment.
Start every headless Chrome with `--mute-audio` and every Android emulator with `-no-audio`
(webshot.mjs and docs/promo/tools/shot.mjs do): this Mac is the owner's desk machine, and an
unmuted test (a video in the viewer, the audio device opening) beeps through its speaker. Kill the
browsers, emulators and VMs a test starts when it ends.
Against a real device without releasing: `VITE_DEVICE=pit.off-the.cloud npm run build --prefix web`,
then `npx vite preview` in `web/`. Under `npm run dev` (Vite's development mode - never `DEV`, which
a build with `NODE_ENV=development` also sets) and at `localhost:4173` (`vite preview`, held to that
port by `vite.config.ts`) the app connects to `wss://$VITE_DEVICE/ws`, or Cala if the variable is
unset; anywhere else, localhost
included (the container image, an SSH tunnel), it uses the page's own host (`web/src/net/endpoint.ts`,
`node web/scripts/endpoint-test.mjs`).

The iOS simulator has no tap or type from the command line, and this Xcode has no Simulator
window to script. Facebook's `idb` drives it instead: `brew install facebook/fb/idb-companion` and
`fb-idb` in `~/.venvs/idb`, then `idb_companion --udid <sim>` and `idb connect localhost 10882`.
Use `idb ui describe-all` / `ui tap x y` / `ui text`, with points, not pixels; the tab bar's
buttons aren't in the accessibility tree, so tap them by position. Decline photo access in a
simulator signed in to a real device, or its sample photos get uploaded to it. Android's emulator
(`otc_pixel`) is driven with `adb shell input` and `uiautomator dump`.
Build the iOS unit tests (`xcodebuild test -only-testing:OffTheCloudTests`, a simulator of your own)
with the simulator's usual signing, never `CODE_SIGNING_ALLOWED=NO`: unsigned, the app has no
Keychain, the address and password it saves read back empty, and the sign-in suites
(`StalledSignInTests`, `StorageUnavailableTests`) fail on a correct tree. The iOS target bundles
`app/ios/OffTheCloud/web-dist` (git-ignored; `cp -a web/dist/* app/ios/OffTheCloud/web-dist/`
after a web build), which a fresh worktree lacks.
```
npm run dev --prefix web       # local dev server
npm run build --prefix web     # tsc -b && vite build
npm run lint --prefix web      # eslint .
```

## Architecture

### Protocol: one WebSocket, protobuf-framed RPC

There is no REST API for app functionality (only `/check_healty` and static file serving exist as
plain HTTP). Everything else — file sync, photo search, social feed, friendships, settings, bridging —
goes over a single WebSocket endpoint (`/ws`) using protobuf messages defined in
`proto/messages.proto`.

- Every request is a `ReqEnvelope{ id, oneof payload }`; every response is a matching
  `RespEnvelope{ id, error, error_message, oneof payload }`. The `id` correlates async
  request/response over the one socket.
- Regenerate bindings with `make pb` after editing `proto/messages.proto` — generated Go lives in
  `proto/generated/`, generated TS in `web/src/proto/`, generated Swift in the iOS/macOS app dirs.
  Don't hand-edit generated files.
- Adding a new RPC = add a message + a case in both the `ReqEnvelope`/`RespEnvelope` `oneof`s in the
  proto, regenerate, then add a `case *pb.ReqEnvelope_ReqXxx:` in the connection handler switch (see
  `websocket/websocket.go`, `processNonAuthRequest`/the authenticated equivalent) and a client-side
  call in `web/src/net/ws.ts`. Also classify it in `websocket/demo.go`'s `demoRules` (allow,
  answer ok without storing, or refuse): `TestDemoRulesCoverEveryRequest` fails until it is.
- **Demo device** (`[otc] demo = true`, docs/demo-device.md): a public device that refuses every
  change with `error_code` "demo" (`dev.err.demo`), gated in `processMessage` before the three
  switches. It publishes `PubKey.demo`/`demo_password` before sign-in and `Status.demo` after, leaves
  `Status`/`Settings.language` unset (each visitor keeps their own, as with an older device),
  answers GetLocalEndpoint `local_unavailable`, refuses a sign-in that would set the owner password,
  and reports a Pi-like Status (8 GB, two 2 TB disks in sync; real library usage). It answers what
  Settings reads (bridge without the server's address, Tailscale, the update check kept ten minutes
  per language, empty disk and Wi-Fi lists, a Wi-Fi status of `managed` on a cable so Settings ›
  Wi-Fi shows - JoinWifi is refused); GetLogs stays refused. The demo is for the web app
  only (the owner, 2026-10-16: no demo in the phone apps; the website links to demo.off-the.cloud):
  `net/demo.ts` follows PubKey/Status.demo; signed out, "Try the demo" signs in with
  `demo_password`; signed in, a banner. Every control and Settings section shows (Logs aside), and
  a change tried shows the notice `web.shell.demo.refused` before any dialog (`refuseOnDemo`), while
  `ws.ts` answers any change request that still gets that far as the device would, unsent; likes
  stay on the page alone.
- The frontend's `web/src/net/ws.ts` / `useWS.ts` wrap the same protobuf envelope pattern for the
  browser client.
- Replies over 64 KiB (`cReplyPiece`) leave the device as one fragmented message in 64 KiB pieces
  (`writeReply`), and the bridge fragments its writes to clients above 256 KiB: every client must
  keep accepting fragmented messages.
- Passwords never cross in the clear (issue #2): each connection gets an ephemeral RSA-2048 key
  (`GetPubKey`, `rsaKeyPool`: one generator, a few made ahead; taken in turn by address -
  `keyTurns`, one connection of an address (`anonBucket`) waiting at once, so connections opened to
  ask for keys put another address's sign-in behind one of theirs, not all - and never for a
  connection that went while it waited), and the clients seal `ReqAuth`'s and `ReqChangeKey`'s keys with
  RSA-OAEP-SHA-256 (`pwCrypto.ts`, `PwCrypto.swift`, `PwCrypto.kt`, otc-sync's `pwcrypto.go`). So
  are `ReqBridgeSignIn.password_sealed` and `ReqSetupTailscale.auth_key_sealed` (2026-10 review,
  `sealedOr`): the device still reads the plain `password`/`auth_key` when no sealed one comes (older
  pages; the phones' Tailscale sections still send `auth_key` plain), the web sends only the sealed
  one (the device serving the page is the one it talks to), and the key reaches `tailscale up` only
  as a file (`--auth-key=file:`): the device hands it to the root Tailscale runner as a 0600 file
  on its RuntimeDirectory tmpfs (`/run/otc/tailscale-auth.key`, issue #204 under "Cutting a
  release"), never on a command line. That
  key is not authenticated: the bridge terminates TLS, so a compromised bridge node could hand out
  its own key and read the password at sign-in. The owner reviewed this on 2026-10-05 and accepts
  it - the bridge nodes are the owner's own and `servercheck.sh` watches them; pinning a device
  identity was judged not viable. Reviews should not raise it again.
- File sizes (issue #187): `File.size64` (int64, field 11) is a file's size in bytes; `File.size`
  keeps that size wrapped to int32, exactly what it carried before, for the apps already in the
  stores. Devices before release 93 leave `size64` at 0, so every reader takes `size64` when it is
  non-zero and falls back to the signed `size`. Device: set a `pb.File`'s size only through
  `dao.SetFileSize(f, n)` and read it with `dao.FileSize(f)` - never set `Size` alone, and never
  scan a size column into the int32 field (a BIGINT above MaxInt32 fails the whole scan;
  websocket's `Init` has a `fileSize` alias because its `dao` parameter hides the package).
  Clients read it through one helper each: web `fileSize()` (`web/src/net/fileSize.ts`; ts-proto
  makes `size64` a required bigint, so a hand-built File literal needs `size64: 0n`), iOS
  `Msg_File.fileSize` (`formatByteCount(Int64)` for display), Android `net/FileSize.kt`
  (`byteSize`, and `sizeMatches(local)`, which compares the wrapped `local.toInt()` with `size`
  only when `size64` is unset - PhotoSync's size shortcut), the Mac's `FileMsg.fileSize`
  (`WSClient.swift`) and otc-sync's `engine.fileSize`. Never read `.size` of a File directly.
- Photo search pages (2026-10-06): `SearchPhotos.limit` (field 8) is the most photos a page may
  hold. The web's grids send 12 on the request that starts a search (no token) so the first page
  paints fast over a slow upload, and no limit when continuing with a token. The iPhone and Android
  apps keep a persistent LRU cache of grid thumbnails (release 113; iOS `ThumbDiskCache.swift` +
  `GridThumbLoader.swift`, Android `ui/common/ThumbDiskCache.kt` + `GridThumbs.kt`): keyed by content
  hash with a kind (small/big/unknown - small replaces either, big replaces unknown), scoped per
  device endpoint, 1 GB by default (Settings > Thumbnail cache: 250 MB-5 GB, usage, Clear), wiped at
  Log Out. They ask pages with `omit_thumbnails` - 12 on a first page until this device has shown
  it omits them, then 60, and 120 when continuing - and fetch only the tiles they lack with
  `GetThumbnails` (only tiles on screen or within reach of the scroll, batches of 24, on-screen
  first, Patience kind "tiles"); an old device ignores omit and its inline thumbnails go into the
  cache. A cached big/unknown one is re-asked once when a row says a small one exists, and one that
  comes back big again isn't re-asked for 7 days; a deleted photo's entry is discarded. iOS reads
  and writes nothing while protected data is unavailable (locked phone, files are
  completeUntilFirstUserAuthentication). Full-size photos (2026-10-24, `FullPhotoCache` on both,
  in `Caches/full-photos` / `cacheDir/full-photos`): the viewer keeps GetFile's bytes (a HEIC comes
  as JPEG) by the answer's `File.hash`, looked up by the item's, so reopening a photo decodes it
  from the phone, nothing asked. Processing tells a HEIC as GetFile does (`isHeicFile`: `.heic` in any
  case, or `image/heic`) - it read only `.HEIC`, so a Samsung's `.heic` photos got thumbnails on their
  side under an upright full size until release 170 (Reprocess Media re-makes those;
  `TestProcessingARotatedHEICWritesUprightThumbnails`). Those bytes are the original's, so a JPEG carries its EXIF
  orientation and the app turns it when decoding: Android's `decodeBitmap` (`ui/common/Util.kt`)
  uses ImageDecoder, never BitmapFactory, which ignored it and showed the Fold's photos on their
  side under upright thumbnails (2026-11-05); iOS's `decodeForDisplay` (CGImageSource with the
  transform) and the web's `<img>` turn them too. A tenth of the Thumbnail cache size picked (the thumbnails get the
  other 90%, Settings' usage line adds both), least recently used out, one photo at most a quarter;
  session only - emptied at launch, by Clear and at Log Out. The open photo is fetched alone; once
  it shows and the viewer stays on it 250 ms, the next then the previous (one at a time, a
  neighbour on its way waited for, never around a video) - started with it they slowed it down. The
  web does the same in memory (Full-size photos on the web, below). The device answers min(limit, its default `[tagger]
  max-images-search`, 30), never more (a page without thumbnails may be larger, below). Older
  devices ignore it. Grids that start a new search bump a search generation, so a page from the
  old search lands nowhere. A grid's tiles can come as small thumbnails (`small_thumbnails`,
  release 111 - see Thumbnails under `files_manager`).
  Pages without thumbnails (release 113): `SearchPhotos.omit_thumbnails` (field 10) answers the
  same page - same rows, order, token, `have`/serve-again and keep-out behaviour - with every
  File's `content` left out, for the phones' persistent thumbnail cache (by content hash, small or
  big), which then asks `GetThumbnails` for what it lacks. The #147 check is a stat
  (`gridThumbnailState`: the big thumbnail exists; with `small_thumbnails` also whether a small
  one is stored - queued for `thumbnail_fix.go` when not - which the row's `thumbnail_small`
  reports), never a read: a thumbnail there but unreadable is listed (and then absent from
  `GetThumbnails`, or a small one answered by the big one) where a page with thumbnails leaves it
  out - the only difference. Such a page may be larger: `pageSize` honours a limit up to
  `cMaxPageWithoutThumbnails` (200 rows, ~40 KB and two stats a row; with thumbnails a limit only
  ever shrinks the page, and 0 is the default either way; `SearchPhotos.limit`'s comment says so
  too). A token's pages may mix both kinds. Every `newSearchTest` case (serve-again, #147,
  keep-out, retries, jump, delete) runs in both kinds (`bothPageKinds`);
  `TestImageSearchPagesThroughItsToken` and `TestImageSearchLimitShrinksOnlyItsPage` run with
  thumbnails only, and `TestImageSearchPagesMixingOmittedThumbnails` pages and sizes with omit;
  `websocket`'s `TestSearchPhotosPassesItsThumbnailFlags` (in a child process: it loads a config,
  which `cfg` can't unload) checks the handler doesn't swap the two flags. Devices before 113
  ignore the flag and send content in pages of their default: clients take the content when an
  entry has it and fetch only entries without.
  A page asked again is served again (2026-10-08): every grid retries a failed page with the same
  token and `have` (`SearchPhotos.have`, field 6: photos it holds), and the device used to answer
  with the page after it, so a page whose answer was lost (a timeout dropping a late answer, a
  socket closed mid-reply, the bridge's 90 s answer) left a hole in the grid for good. A token
  (`files_manager/search_tokens.go`, `searchCursor`) now remembers where its last page started
  (`prev`), the `have` it was asked with (`before`) and the count it leaves (`after` = before +
  its photos). `resume`: `have == after` goes on; `have == before` serves that page again from
  `prev` (a #147 row without a thumbnail then may now come, and the page then ends earlier: what
  no longer fits comes first on the next page, nothing skipped), but only once the client is in
  step (`synced`: that page was asked for with the previous page's `after`; a page of a search
  resumed from an unknown token that brought only photos the client had must go on), at most
  `cMaxServedAgain` (5) times in a row, and only if no file was deleted since that page was served
  (`delFile` counts every delete, `noteDelete`: a client lowers `have` by what it deletes, so one
  that deleted as many photos as the page brought asks with `before` too); `have == 0` (clients
  that don't send it, older apps) and any other count go on, as before. The rows from `prev` on
  stay in the token (`pageCursor` compacts only what came before; a token holds
  only what it has fetched, below), and `keepOut` (#192) filters them too. Each search takes a sequence number as it starts (`begin`), and `store` refuses a
  cursor behind the stored one by (`pages`, `seq`): a request finishing late can't move a token
  back, and of two requests for the same page (the first still being served when the client's
  retry came) the token keeps the one started later - the retry, whose answer the client keeps.
  A last page stores nothing, so asking it again goes on from the same place. Every grid and
  picker sends `have` with token requests (web `PhotoGallery`, `NewPostPicker`,
  `DevicePhotoPicker`; Android `PhotoGalleryViewModel`, `NewPostPickerView`, `DevicePhotoPicker`;
  iOS `PhotoGallery`, `NewPostPicker`, `DevicePhotoPickerView`); with no token it means nothing.
- A photo at several paths (issue #201: typically downloaded to a phone or computer and synced
  back, with the date it came back): Images lists it once, at its **oldest copy** (earliest
  `created`, then the smallest path) among the copies it may show (outside the folders kept out
  of Images; under the folder for a folder gallery). Chosen when listing; no stored date is
  rewritten for it (dates taken, below, move a media row's date for another reason), Files lists every
  path with its own. `dao.SearchMedia` reads every such row newest
  first and, at one instant, by path backwards (a tag search by score first: tags and people are
  settled per hash in derived tables, so a tag's score is no longer multiplied by the copies and
  nothing relies on lax GROUP BY) and keeps each hash's last row (`oldestCopies`); the plain
  browse (below) picks the same row in SQL, the one with no older visible copy of its hash. `before`
  stays a per-row SQL filter, which is equivalent; #194's `newer_than` must filter after
  `oldestCopies` (or compare the oldest copy's date), never per row. The date buckets count each photo once, in its oldest copy's month (`min(created)`
  per hash; kept as counts since release 168, issue #206 below), and collections count and draw
  their cover per member (EXISTS), whatever the copies.
  Every SearchPhotos entry carries `File.copies` (14, `optional`): the `files` rows holding its
  hash, kept-out ones included, versions not, counted per page as served (`setCopies` ->
  `dao.CountCopies`, an index lookup). A count that fails doesn't fail the page: it is logged and
  the page served with `copies` unset on every entry (so the apps offer no "every copy" for it),
  its token moving as usual.
  `DelFile.every_copy` (2, `optional`) -> `files_manager.DelEveryCopy`: every path with the hash
  (`dao.PathsOfHash`), each through `delFile` (its versions, the blob with the last, a
  `noteDelete` each), one `reconcileAnalysis`, no flag cleared; all or nothing - a copy (the asked
  path included) under an upload-only folder refuses it as `CopyUploadOnlyError`, `error_code`
  `copy_upload_only`, message `dev.files.copy_upload_only` naming the outermost such folder (never
  logged at Info). `DelFile.hash` (3, `optional`) is the content the client showed for the path
  (the tile's `File.hash`, which is the row's `files.hash`, the plain content's SHA-256): when set
  and the path is a file now holding other content (synced over, edited), nothing is deleted -
  `DelEveryCopy` checks it before `PathsOfHash`, `DelPath` before the upload-only check, with or
  without every_copy - and the answer is `ContentChangedError`, `error_code` `changed`, message
  `dev.files.content_changed` ("refresh and try again"). Ignored when empty and for a folder's
  path. A folder's path is DelPath as before; older devices ignore both fields. Every delete is
  bound to the content it means (2026-11, `delFileIf` -> `dao.DelFileIfHash`, the row read `for
  update` in the delete's own transaction; it deleted by path alone, so a sync app's edit landing
  between the check and the delete was deleted unseen): a file's to `DelFile.hash` when sent (the
  check is the delete's, not only a look beforehand), a folder's files each to what was listed
  under it - one given other content meanwhile stays, and so does the folder's keep-out flag - and
  every_copy's to the content shown, the asked path first: changed, nothing is deleted
  (`ContentChangedError`); another copy changed is no copy any more and stays. A DelFile of ""
  or "/" (every file: neither has a row, so `resolvePaths` took it for the folder of them all) is
  refused before anything is read (`ErrWholeLibrary`, `dev.files.whole_library`): no app's delete
  names it, and a sync client's empty relative path would have taken every file, version and
  content with no way back. Measured
  on 112k rows with 14k photos at several paths (Lima VM, MariaDB 11.8): browsing 291 ms of SQL as
  before (282), the buckets 154 ms (were 327), two people 1.8 ms (5.4), a page's copies 0.3-0.6 ms.
- **Photo edits** (2026-11, `files_manager/edit_image.go`, `edit_exif.go`, `dao/edited.go`, table
  `edited_content`, release 174's `174.sh` and install.sh's block): the viewers turn and crop a
  photo on the device.
  - **Requests:** `EditImage {path, hash, quarter_turns, crop, as_copy, every_copy}` (162),
    `RevertImage {path, hash, every_copy}` (163), `GetEditState {path}` (164) ->
    `EditState {copies, edited}` (resp 77). Older devices answer `unknown_payload`; the demo refuses
    the two writes.
  - **The edit is content of its own**, with a real SHA-256 of its bytes. The owner chose this
    over keeping the photo's hash for "every copy": the sync app checks each download against the
    hash (`engine.go`), and the apps cache thumbnails and full sizes by hash.
  - **The original is kept, with no path.** `edited_content(hash, original_hash, original_mime,
    original_size, original_ext)` holds one row per edit, naming the first original even through
    edits of edits, so a revert is never to a middle edit. `HashReferenced` counts
    `original_hash`, so the original's blob, thumbnails, tags and faces stay while an edit of it is
    held. The tag deletes of `DelFileIfHash` and `OverrideFile` spare it (`editRefs`), and so does
    `OrphanFaceHashes`.
  - **Clean-up:** `removeBlobIfUnused` and `dropIfOrphaned` (now `removeUnusedBlob`/`dropOrphan`
    plus `releaseEdit`, after the hash's lock, since the original's stripe may be the same) forget
    a gone edit's row, then remove its original once nothing else holds it, its tags with it
    (no file's delete took them).
  - **No re-upload, and dedup:** `HasFile` and `HasHashes` count a held original whole
    (`HeldOriginals`, with its size), so a phone never sends it again and Free up space can
    delete its copy. `LinkFile` and `registerUpload` (uploads, FinishUpload) first ask
    `editedAlready` (`dao.EditedFolderFile`): content sent into a folder that has a direct child
    which is an edit of it, with no file there holding that content itself, is answered with that
    edit (a phone's sync sending the photo it took, to its own name or an alt one, or the sync
    app's backup putting it back). The same holds for the edit's own hash at the photo's old name
    when the edit renamed it (an iPhone relinking by cloud id). Nothing is stored twice, and the
    store apps, which know nothing of edits, are covered without a change. Linked anywhere else,
    the original is an ordinary photo again (`originalOf` gives LinkFile its MIME and size).
    The computers' backups (otc-sync's `engine.reconcile`, the Mac's `SyncModel.reconcile`) also
    count a local file backed up when the device's file at its path is its edit
    (`File.original_hash` = its hash), or when an edit that renamed it holds its stem
    (`editedStems`, hashed only then). Without this, each pass would send it again, for the
    guard above to answer.
  - **The pixels:** decoded at full size under `holdDecode` (`decodeFull`; HEIC through
    `decodeHeic`, with its irot/imir or EXIF orientation). The photo's own orientation and the
    turn go in one `applyOrientation` pass (`thenTurned`: the orientations' matrices composed).
    Then `editRect`'s crop (fractions of the turned photo, rounded, kept inside it).
  - **What is written:** a JPEG at quality 92 (`cEditQuality`) with the camera's EXIF (a JPEG's
    APP1, a HEIC's Exif item) carried over by `editedExif`: orientation 1, the Exif dimensions set
    to the edit's, IFD1 (the old thumbnail) dropped. Its ICC profile comes too (APP2 segments, or
    a HEIF's `colr` prof/rICC, believed only with "acsp"), so its date and place come back through
    processing. A PNG is written as a PNG. A JPEG only turned is turned losslessly: only its
    orientation tag changes (`losslessTurn`; one with no tag is written again).
  - **Not editable** (`editKindOf`, by the bytes): camera raws, animated PNG/WebP, anything not a
    JPEG, PNG, still HEIF (`isHeifStill`, by major brand: an MP4's ftyp isn't one) or WebP.
    `error_code` `not_editable`, `dev.files.not_editable`; also before release 174's table exists
    (`errEditsUnavailable`).
  - **Replacing** (`dao.ApplyEdit`, one transaction): the asked path first, then with every_copy
    each path holding the content. Each row is read `for update`, and one holding other content
    meanwhile is left out; the asked one so is `ErrOtherContent` -> `changed`. A name whose
    extension doesn't fit is renamed to `.jpg` (in upper case after an upper-case one:
    `IMG_1.HEIC` -> `IMG_1.JPG`), or the first free `name (n).jpg`. When no file holds the photo
    any more, its collections, Favourites and ratings move to the edit (`moveMembershipsIfGone`:
    update ignore, then the leftovers deleted). "Only this one" leaves them with the photo.
  - **As a copy** (`AddEditCopy`): `<name> (edited)` (`dev.files.edited_copy_name`, a `text` arg:
    no isolates in a file name) next to it, with the photo's `created`.
  - **Faces** go to the edit before its analysis (`dao.CopyFaces`, `editResult.moveFace`): moved
    with the turn and crop, in the analysis picture's pixels (`workScale`: photos over
    `cWorkPixels` are analysed shrunk), each to the same person. A face the crop cut more than half
    off is dropped. `processFaces` then finds them and detects nothing; with none, it detects as
    for any photo. Tags, quality and Smart search are worked out anew (`enqueueMedia`). People's
    counts are of faces in photos a file holds (`ListPeople`), so a hidden original isn't counted.
    The people added by hand (release 179, `people_by_hand`) are copied to every edit, cut and
    compress (`CopyPeopleByHand`, no box to move), and a revert copies the edit's back to the original.
  - **Revert** puts the original back (its name too, `original_ext`, when free), and the edit
    goes once no path holds it. Turning an edit back to the original's very bytes is a revert,
    with no edit row.
  - **Videos** (2026-11-08, `files_manager/edit_video.go`; the owner asked for the post
    composer's cut on videos, and chose a lossless cut over an exact re-encode): `EditVideo
    {path, hash, start_secs, end_secs, as_copy, every_copy}` (165) -> resp_file; RevertImage and
    GetEditState as for photos. ffmpeg copies the streams (`-c copy`, `cutArgs`): nothing lost, HDR,
    rotation, date and place kept (`-map_metadata 0`, `use_metadata_tags`; only the picture and
    sound tracks, `-map 0:v -map 0:a?`, as a phone's timed-metadata tracks may not go back into
    the container). A copied stream starts at a keyframe: an MP4 or MOV keeps the frames from the
    last one before the start behind an edit list (ffmpeg's default timestamps), so players that
    read it start exactly there and one that doesn't starts at the keyframe with the sound in step
    (measured with a flash and a beep, ffmpeg 7.1 reading and ignoring edit lists); an MKV, WebM or
    AVI has none and starts at the keyframe (`-avoid_negative_ts make_zero`). The container stays
    the video's own (`cutFormats` by type, `cutExtFormats` by extension for an unknown type; MPEG-TS,
    WMV, FLV... are `not_editable`, `dev.files.not_cuttable`). `cutRange`: an end of 0, past the
    video's or within 0.05 s of it is the end; under 0.5 s is `too_short` (`dev.files.cut_too_short`);
    the whole video changes nothing. ffmpeg reads the stored video over the loopback stream in a
    post transcode's slot (`transcodeSlots`, `jobTranscode`) and writes a plain `.cut-*` into
    unenc-storage-path (checked for room first; the orphan sweep takes one a power cut left),
    sealed into the storage a segment at a time (`storeEditFile`) and saved by `saveEdit`, the
    photo edit's own path (shared since this): a cut keeps its name (`renames` nil), a copy is
    `<name> (edited).<ext>`, faces are left to the analysis. A failed ffmpeg is
    `dev.files.edit_failed` with `cut_failed`; a revert with nothing to undo on a video says
    `dev.files.video_not_edited`. Tests: `edit_video_test.go` and `TestCutAndRevertVideoMySQL`
    (a real cut with the Mac's ffmpeg against MariaDB, as the photo tests).
  - **Compress** (release 177, `files_manager/compress.go` the options, `compress_job.go` the job,
    `dao/compress.go`, column `edited_content.original_removed` - `177.sh` and install.sh's block;
    the owner asked to free space from Settings › Largest files and Images): a photo or video is
    re-encoded smaller in place of every copy, saved as an edit is, and then **the original's
    bytes are deleted**; its hash stays in the edit's row with `original_removed = 1`, the record
    the phones and computers that keep the original are answered by. There is no revert.
    - **Requests:** `GetCompressOptions {items: [{path, hash}]}` (167, at most 500, else
      `too_many`) -> `CompressOptions` (resp 78): per item, in the order asked, its size, copies
      and `choices` (preset + estimated size, strongest first), or `reason`/`reason_code`, decided
      in this order (`refusal`): `changed` (the path holds another hash, or none; the words of
      `dev.files.content_changed`), `not_media` (by type, or the extension when the type is
      `application/octet-stream`), `upload_only` (any path of the content under an upload-only
      folder), `kept_version` (a `file_versions` row holds it), `missing` (no blob),
      `nothing_to_gain` (no preset at least 15% smaller, or a database without the column).
      `CompressFiles {tasks: [{path, hash, preset}]}` (168) -> `CompressJob` (resp 79) at once;
      `GetCompressJob {id}` (169; "" the current or last) -> the same, `not_found` when there is
      none. Demo: the two reads and the options allowed, CompressFiles refused. `RevertImage` of a
      compressed file is `original_removed` (`dev.files.original_compressed`); `EditState.
      original_removed` (3) and `File.original_removed` (17) go with `original_hash` everywhere
      (`setOriginals`, `EditedFolderFile`, saveEdit's answer).
    - **Presets** (from the header only - photos: `jpeg.DecodeConfig`, the PNG IHDR and its chunks
      before IDAT, the HEIF primary item's `ispe`, WebP's flags, read from the blob's first 1 MB
      segment, the whole photo only when the header runs past it; videos: ffprobe over the loopback
      stream, four at a time; cached by hash for the job, 1000 at most - `compressProbes`):
      `video_720p` (shorter side over 800), `video_1080p` (over 1200), `video_hevc` (a codec other
      than hevc, av1, vp9); estimate = duration x (video target + sound) / 8 + 1%, targets for 30 fps
      HEVC 3 and 6 Mbps scaled by min(2, max(1, fps/30)) and never over the source's own rate,
      `video_hevc` 0.55 x the source's, sound its own rate when AAC (copied) else 160 kbps. Only an
      MP4 or MOV (an M4V is an MP4) - ffmpeg's 3GP muxer refuses HEVC, so a 3GP, an MKV, a WebM get
      nothing; none at all when the device's ffmpeg has no libx265 (`hasX265`, asked once), nor on
      the 2 GB tier (`MemoryProfile.VideoCompressAllowed`: ffmpeg alone took 0.7-1.2 GB on 4K).
      `photo_12mp` over 13 MP (12e6 x 0.32 bytes), any photo the edits decode; `jpeg_resave` a JPEG
      over 0.45 bytes a pixel (pixels x 0.24); `png_to_jpeg` (pixels x 0.30). A PNG or WebP with a
      transparent pixel gets nothing (a JPEG has none): a PNG whose header allows it is decoded to
      see, within the decode budget. No camera raws (`editKindOf`).
    - **The job:** one per instance, in memory (`compressState`): tasks sent while it runs join
      it (a path waiting or running isn't queued twice); a finished one is kept until the next
      starts; a restart forgets the tasks not started. One worker, a task at a time, listed in
      Status as `compress` (`BackgroundCompress`, `dev.status.background.compress`), each through
      `beginProcessing("", jobTranscode)` and a video in `transcodeSlots`. Each task checks again
      (the hash - an empty one is `changed` - upload only, versions, the blob, the preset still
      offered: `dev.files.compress_unknown_preset`), checks room for 1.2 x the estimate, then: a
      video with ffmpeg into a plain `.compress-*` in unenc-storage-path (the orphan sweep takes
      one a power cut left) - `-map` the picture and each sound ffmpeg knows, `libx265 -preset
      faster -b:v T -maxrate 1.5T -bufsize 2T -tag:v hvc1`, `scale` of the shorter side to 1080/720
      on the frames as shown (ffmpeg turns them upright as it re-encodes: a phone's portrait video
      comes out 1080x1920 with no rotation), `yuv420p10le` for a 10-bit source with its
      primaries/transfer/matrix, AAC copied (`-c:a:N copy`, else `aac 160k`), `-map_metadata 0
      -movflags +faststart+use_metadata_tags` (date and place kept), the decoder's threads as
      `ffmpegInput` sets them (x265's pool left to its own size: `pools=N` measured no less memory
      and asks for a NUMA affinity a container refuses), progress from `-progress pipe:1` into
      `percent` (never backwards),
      40 s a second of video, 30 min at least (`compressVideoArgs`, `compressRun`); a photo in
      memory (`renderCompress`: `holdDecode`, `decodeHeic`/`decodeFull`; `photo_12mp` by a whole
      factor with `shrinkBy` - the analysis' box, on the YCbCr - when it is 2 or more, then
      Catmull-Rom the rest of the way, then turned upright, JPEG q88; `jpeg_resave` q82 and
      `png_to_jpeg` q90 keep the pixels and their orientation tag; the EXIF through `editedExif`,
      the ICC from APP2, a HEIF's `colr` or a PNG's `iCCP`/`eXIf` - `pngMetadata`). A result over 95%
      of the original is `skipped` (`dev.files.compress_not_smaller`). Then `saveEdit` with
      `removesOriginal`: `dao.ReplaceEveryCopy` - every path of the content in one transaction, and
      when a file or kept version still holds it after (a copy linked meanwhile) nothing changes,
      `changed`; the edit's row is written with `original_removed = 1` (only then is the column
      named, so a database without it still takes ordinary edits; one that hasn't it refuses the
      compress); an edit's source names its first original; faces copied scaled (`scaledFace`, in
      the analysis' pixels; a video's left to the analysis); collections, Favourites and ratings
      moved; `enqueueMedia`. Last `dropRemovedOriginal(original)`: under the hash's lock, only for
      content an edit's row marks removed and that no `files`/`file_versions` row holds
      (`dao.ContentHeld`; `HashReferenced` always says yes while the row names it) - every edit of
      it marked removed (`SetOriginalRemoved`), then its blob, thumbnails, tags, faces, place,
      measures, embedding and pending analysis deleted; held after all, its edits are ordinary
      again (revertible). An intermediate edit's blob and row go with saveEdit's
      `removeBlobIfUnused`. `saved` += what was freed less the result. Processing of the original
      in flight when it went is cleaned by `dropIfOrphaned`, and a compress a restart cut between
      the save and the delete is finished at the next start (`finishRemovedOriginals`, after the
      orphan sweep).
    - **Alerts** at a job's end, never pushed (`dao.AddStandaloneAlert`, a standalone Error row
      with lines): `dev.alert.compress.done` (count, the bytes freed as `i18n.FormatBytes` in the
      language of the request that started the job) and/or `dev.alert.compress.failed` (count),
      one line a failed file - `dev.alert.file.not_processed` with the file's name and the reason's
      key, "14:05 IMG_1.JPG could not be processed: <why>" (no catalog key names a file alone).
      Skipped files are in neither.
    - **An original that is gone** (`compressedFor` = `dao.RemovedOriginalEdit`: the newest edit
      marked removed, at its first path, or as a version keeps it; nil while a file holds the
      original): `HasFile` answers true (the phones don't send it); `HasHashes` never counts it,
      skipped before `wholeBlob` (Free up space never deletes the phone's copy); `originalOf`
      says it is removed, never held; `LinkFile` answers `editedAlready` in its folder and links the compressed
      content anywhere else, never "no such hash"; `UploadFile` doesn't write it (and
      `FinishUpload` aborts its sealed file) when it is such an original, `registerUpload`
      answers as LinkFile, and bytes written by an upload that raced the compress are removed
      (`dropRemovedOriginal`). An edit of a compressed file keeps the mark (`saveEdit` carries
      `prev.OriginalRemoved`).
    - **Tests:** `compress_test.go` (estimates table, kinds, reasons, the token, ffprobe's answer,
      ffmpeg's arguments, headers, the PNG's metadata, the render, the shrink) and
      `compress_mysql_test.go` (`TestCompressMySQL`: a 4K HLG clip held upright and a 48 MP JPEG
      through the options and the job against MariaDB with the Mac's ffmpeg - every copy, the
      originals' blobs and thumbnails gone, faces, collections, the alerts, HasFile/HasHashes, an
      upload whole and in chunks, LinkFile, Revert refused, an edit after; and the start's finish),
      `dao/largest_mysql_test.go`, `updater/release177_test.go`. End to end on 2026-11-09 in a
      throwaway image of the branch (Debian's ffmpeg 7.1 with libx265; held to 5.75 GB and 4 CPUs
      of the Mac's, memory locked): a 45 s 4K HEVC recording at 40 Mbps to 1080p in 28-34 s,
      227 MB -> 34.9 MB (estimated 34.8), ffmpeg at ~690 MB and the container at 1.42 GB; a 3 s
      4K HLG clip held upright to 1080x1920 Main 10 with its colours, ffmpeg at 1.16 GB at its
      peak; twelve 48 MP HEICs at 2-4 s each, 15 MB -> 1.37 MB, the device at ~530 MB; the 17
      files' options in 70-100 ms; 370 MB freed; the originals' blobs and thumbnails gone,
      HasFile true, HasHashes none, an original sent again in chunks answered with its compressed
      file and stored nowhere, Revert `original_removed`; no OOM. A Pi 5's cores are several
      times slower than these.
  - **Listings:** ListFiles, SearchFiles and SearchPhotos pages set `File.original_hash` (16)
    (`setOriginals`, one query, a failure only logged) for the viewers' Revert to original, and
    `original_removed` (17) on a compressed file.
  - **Tests:** `edit_image_test.go` (the maths, EXIF, ICC, formats) and
    `edit_image_mysql_test.go`, against a real MariaDB: `OTC_TEST_DEVICE_MYSQL_DSN=user:pass@tcp(host:port)/`,
    a user that may create databases (it makes one from db.sql and drops it).
- Dates taken (release 152's `updates/152.sh`; `files_manager/date_taken.go`, `exifinfo/taken.go`): for a
  photo or video, `files.created` is the date it was taken whenever its own metadata clearly says the row
  is a later copy - a library copied to a drive in 2018 and synced from there (the sync clients send a
  file's creation time, #134) no longer sits in 2018. The rule (`createdFromTaken`, table-tested): with T
  the date taken and C a row's `created`, the row moves to T when T < C - 26 h (more than the widest gap
  between two zones' clocks, so a phone's exact capture time never moves), never later; a T outside
  [1990-01-01, now + 1 day) or a clock reset (1 January at exactly 00:00:00, any year) is ignored.
  `exifinfo` reads T the same in any zone (goexif's `DateTime()` used `time.Local`): DateTimeOriginal,
  else DateTimeDigitized, else IFD0's DateTime; made exact by OffsetTime(Original/Digitized) (EXIF 2.31,
  read from the Exif sub-IFD, which goexif skips), else by the GPS clock (UTC: the gap to the nearest
  quarter hour, when a zone can have it, -12 to +14 h), else the clock read as UTC (`Info.TakenClock`
  keeps the clock as written); a video's `creation_time` is UTC. GetFileInfo's taken time is the same
  instant. Applied where the metadata is read: `processMedia` (both lanes and Reprocess; before the
  decode, so a photo the device can't show gets it too; an AVIF, or a HEIF under another name, read by its
  boxes for this),
  and once for what a database held before: `BackfillDatesTaken`, after `FitStoredThumbnails` in
  `startBackfillOnce` (before Places and On this day), reads every media hash (`ListMediaForReprocess`,
  500 a page) up to its metadata only (`readStoredMetadata`, shared with Places: a photo up to its EXIF,
  a video by ffprobe on its stream), 20 ms after a photo and 250 ms after a video, behind `waitForQuiet`
  (it stops for a reprocess), saving its place in `settings.dates_taken_cursor` after each page and
  setting `dates_taken_backfilled` at the end; a database without them is skipped (`ErrSchemaBehind`);
  content not read this time is counted, not retried (Reprocess applies the rule); one Info line at the
  end. Each read is the processing guard's job (`beginProcessing(hash, jobMetadata)`, as Places'
  reads are): after a death the content being read is recorded in flight, so what takes the process
  down twice is set aside (runstate.go) instead of stopping the pass at every start, and set-aside
  content is passed over (the processing that tries it again applies the rule); a parser's panic is
  content not read (`readMetadataSafely`, for both passes). Lima VM: 311 stored photos and videos in
  9 s, ~7 ms of work a photo besides the pause. Each application is one `dao.MoveToDateTaken`
  (`update files set created = T where hash = ? and created > T + 26 h`: every `files` row of the
  content, rechecked in SQL; `file_versions` keep theirs), then
  `memories.changed()` and `places.changed()` when a row moved. So Images' order, month counts, the
  scrubber, `before`/`newer_than`, the oldest copy (#201), On this day and Places' covers have the date
  taken with no query changed; Files' Created column shows it too, and the sync clients give it to a file
  they download as its creation time. A path added later for content already processed (not processed
  again) keeps its own date; Images still shows the oldest copy. Known limit: a photo with neither an
  offset nor a GPS clock is placed at its clock read as UTC, and the apps show `created` in the viewer's
  zone, so east of UTC its last hours of a day show on the next day (one or two in Spain), west of UTC
  its first hours on the day before. Tests: `date_taken_test.go` (`TestMetadataPassesRecordWhatTheyRead`
  too), exifinfo's `taken_test.go`, dao's `TestMoveToDateTakenMySQL` and `TestDatesTakenBackfillMySQL`.
- EXIF is never parsed from a file's own bytes (2026-11, `exifinfo/tiff.go`): goexif works out a tag's
  size in 32 bits and then allocates for its count - a 48-byte JPEG asked for 4 to 16 GiB, an
  out-of-memory death no `recover()` catches - follows a chain of directories that name each other
  forever, and decodes every tag of every directory, however many share the same bytes (a 64 KiB
  segment of them took 812 MiB). `compactTIFF` reads, with 64-bit bounds, only the tags `fromExif`
  reads (`keptMain`, `keptGPS`, `keptOffsets`: a new field read there goes in them), from where goexif
  takes them - IFD0 and the Exif sub-IFD into one table, the last tag of an id winning; the GPS
  sub-IFD; the offset tags of the directory that table names last - at most the last of each id (8
  of an offset's), each value cut to 4 KiB, and writes them into a small TIFF of its own, which is
  what goexif decodes: a few hundred KiB whatever the file says. A malformed tag or directory is
  left out on its own (goexif refused the whole file). Every reader goes through it (`FromJPEG`,
  `FromJPEGReader`, `FromHEIC`, `FromHEICReader`: uploads, More info, the dates' and Places' passes,
  On this day's camera check, shared galleries, Reprocess). A JPEG's segment is found by following
  its markers - past XMP's own APP1, which goexif took for EXIF's - then byte by byte from where they
  stop making sense, as goexif looked (a camera raw with a JPEG inside); a TIFF (a scan, a camera
  raw) is read at its directories through the blob's `ReaderAt`, wherever they are, never whole
  (it was parsed whole, three copies: `exifOfBlob` holds `cExifReadBytes`, 8 MiB, of the content
  budget instead of three times the file); a goheif panic on a short Exif item is an error. Checked
  against goexif itself on its own samples, x/image's test images and the fixtures: the same `Info`
  for every file goexif read, and one more read (a TIFF with a short tag goexif refused whole);
  `TestCompactReadsWhatGoexifRead` keeps the fixtures' and every field's case, and
  `TestCraftedEXIFIsCheapToRead` reads the crafted shapes as a JPEG, an Exif item and a TIFF within
  8 MiB each.
- Scrolling up after a jump (issue #194): `SearchPhotos.newer_than` (11) asks for the photos dated
  strictly after the instant, served OLDEST first, and every answer to it has
  `ListOfFiles.oldest_first` (6) true - its last page and an empty one too; devices before #194
  ignore the field and answer newest first with it false, which the apps drop (and stop asking
  until the next sign-in). `dao.SearchMediaNewerThan` runs the plain search's query (no date in
  it) and keeps the photos whose oldest copy is after the instant, after `oldestCopies`, turned
  round (a tag search, by score, is sorted by date then path). With the same instant `before`
  and `newer_than` split the library exactly: both compare at the second (`searchCutoff` -
  `created` is a DATETIME; MariaDB truncates a fraction, MySQL may round it into the next second).
  Unlike `before`, the client sends it on every page and it pages like any search: token, `have`
  (only what the upward search brought), served again, keepOut, #147, limit, small/omit. A token
  remembers its instant (`searchCursor.newerThan`, `sameDirection`): one of the other direction
  or of another instant counts as unknown (searched again past `have`). It wins over `before`.
  An upward search is read as it scrolls (below), filters and all: ~1 ms for its first page
  on 200k photos, where it used to cost one whole plain browse query (280-365 ms of SQL on the
  112k-row library above), and its token holds only what it fetched above the jump. Every
  `newSearchTest` case also runs going up (`everySearchKind`: the upward mock is dated so its
  pages are the case's paths in order, with rows at the cutoff's second and a photo synced back
  above it that must never show); `TestImageSearchNewerAndJumpSplitTheLibrary` (and its `MySQL`
  twin, end to end) and dao's `TestSearchMediaNewerThanSplitsWithBeforeMySQL` check that the two
  halves are the plain search, each photo once at the same copy; websocket's
  `TestSearchPhotosNewerThanAnswersOldestFirst` (a child process) checks the handler.
- Every Images search read as it scrolls (issue #206; `dao/browse.go`, `dao/search_lists.go`,
  `files_manager/browse.go`): opening Images, a jump, scrolling up after one, the pickers, a tag, a
  person, a collection, a map area, a text, a rating and a folder's gallery no longer read every
  row of the search when it starts - 200k photos were 143 MB allocated, 82 MB kept by the token and
  seconds on a Pi before the first tiles. `searchFor` picks how a new search is read; every way gives
  `SearchMediaRated`'s photos (`SearchMediaNewerThanWithin`'s going up), copy for copy, in its order.
  - In date order (the plain browse, people, a collection, an area, a rating's filters, a folder, a
    tag going up), by keyset: `dao.BrowseMedia` reads it a chunk at a time by (`created`, `path`)
    with no schema change: `files`' `created` index (since the start) and DATETIME's whole seconds.
    A chunk is two statements: the dates of its photos and the one after them, in index order
    (`order by created ... limit want+1`, no filesort; the rows passed over - copies whose photo is
    older, documents, what the filters leave out - are read once, there), then every photo of those
    whole seconds sorted in SQL by (`created`, `path`). Both read `files` first (`straight_join`)
    and look each row's filters up by index (`rowClauses`): the collection, its ratings and the area
    are joins on unique keys, tags and people `(select 1 ... limit 1) is not null` (an EXISTS became
    a semi-join that read the person's or the tag's table whole at every chunk; ids listed twice, or
    "", match nothing, as `count(distinct person_id)` did). A photo is the row with no older visible
    copy of its hash (`not exists` on `g`, `visibleRows`: in the folder, outside `excluded`, media
    when no tag, person or collection ties the rows to it; older = earlier `created`, or the same
    second and a smaller path) - `oldestCopies`' choice; `before` is per row, going up `created >
    cutoff` is the oldest copy's (and, as `SearchMediaNewerThanWithin`, no rating). A second with
    very many rows makes its chunk as large (at worst the library, as before).
  - A date-ordered search whose filters keep few photos is read through a list instead: by keyset
    it would read most of the library before its first chunk (a person in 100 photos: 300 ms; a
    collection imported years down: 400 ms). `dao.EstimateMedia` counts each filter's rows by index
    (an area's by a pass over the positions, ~10 ms), and at most `cListMax` (30,000)
    `dao.DatedMedia` reads the smallest filter's hashes (a derived table, `straight_join`) with
    their rows' dates - each photo's oldest copy's second, picked in Go - once; past it, keyset.
  - Ranked by its tags' score (going down) or its collection's stars: `dao.RankMedia` lists the
    photos by what ranks them, from the tags', faces', collections', ratings' and places' tables
    alone (never `files`, whose rows a chunk judges): from the collection's members by stars, else
    from the tags' rows summed per hash, or - when another filter keeps fewer (the estimate) - from
    that filter's hashes with the score summed by the (hash, tag) index (the optimizer had read both
    tables whole: 486 ms, now 102 ms). A sum of three tags or more may differ from the whole
    search's in its last bit (the order it is added in).
  - A text (Smart search): `textList` scores the photos' vectors (`textVector`, `cosineLocked`, as
    `rankByText`) - every photo described, or with filters that keep few only those of the
    filters' own list - keeps those at or over `cSmartFloor`, best first; the first chunk that finds
    a photo sets `rankScores`' cut (`cutAt`), and the token then keeps only the list over it.
  - A list's chunk is `dao.MediaListChunk`: the next hashes, at least the photos asked for, every
    photo that ties on what orders them (a second, a score, stars, a cosine) included, their rows
    through `SearchMediaOf` - `searchMediaOnlyClauses`, the whole search's clauses with its derived
    tables cut to those hashes (`only`), at most `cMaxIn` (1000) to a statement, sorted in Go as SQL
    did (utf8mb4_bin compares bytes) - in the list's order (`ListOrder`: dated, by the list's ranks,
    or a text's cosines with ties in the search's order).
  The token (`searchCursor.more`, a `browseState`: the query, where the next chunk starts - keyset
  or a list's place and end -, done, and `dropped`) holds what it fetched and a list's hashes (the
  cache's cap counts `cListRows` hashes a row, `searchCursor.weight`): a new search fetches only what
  its page shows (past `have` for an unknown token), a page that runs out fetches what it still needs
  and a chunk ahead (`browseChunk`, 240), and a page that served all it held looks past itself so
  its last page ends the token as before (a look that fails leaves the page a token; a fetch a page
  needs fails the request, asked again as a lost one). Arrays a stored cursor holds are never written
  (`fetchMore` appends to a clipped slice: #171). `keepOut` adds its folders to `dropped`, which
  later chunks leave out; the query keeps the folders kept out when the search began, so each photo
  stays at the copy it began with, as for a whole search. What differs is only when rows are read: a
  whole search was a snapshot, while a chunk is read when needed, so a photo added below where a
  scroll has got to comes in that scroll (a list's photos are those it found when the search began)
  and one deleted meanwhile doesn't. A `Manager` without `browse` (Init sets it to the dao's
  `BrowseMedia`; `readsAsItScrolls`) still reads every search whole: every sqlmock search test pins
  that one-query plan and runs unchanged.
  - Month counts (the date scrubber; `dao/months.go`, release 168's `updates/168.sh`): with no
    filter, kept as the library changes instead of grouping every row by its content at every
    Images opening. `photo_months` is per month (year * 12 + month - 1) how many photos have their
    oldest copy outside the folders kept out in it (`images` among image rows, `media` image and
    video rows); `photo_month_hashes` each content's two months; `settings.photo_months_for` the
    folders they were built for (outermost, sorted, JSON; NULL: not built). Every dao write of
    `files` (`StoreNewFile`, `DelFileByPathHash`, `OverrideFile`, `ReplaceFileKeepingVersion` - which
    reads the old hash for it -, `MoveToDateTaken`) marks the contents it touched, in memory only
    (`markMonths`: no statement more, so no sqlmock test changed); before the counts are read, and
    every minute (`keepMonths`), each marked content's months are worked out from its rows and
    `photo_months` moved by the difference from `photo_month_hashes`, one transaction per 500
    (`reconcileLocked`): state, not deltas, so any order or number of writes comes out right. Other
    folders kept out, or no counts built: counted the old way while `RebuildMonths` builds them in the
    background (one at a time; reads never wait for it). The marks live only in the process:
    `settings.photo_months_run` is the process keeping them (`LeaseMonths`, in Init before anything is
    written); finding another's there (a run that died with marks not worked out) throws the counts
    away, and a clean stop (`dao.Stop`) works its marks out and gives the lease back. A script that
    changes `files` rows must set `photo_months_for` to NULL. Filtered counts read the smallest
    filter's photos (`filteredMonths`, `datedRows`) instead of the library. Without the columns
    (`ErrSchemaBehind`) nothing is kept and the counts are the old query's.
  Tests: dao's `TestBrowseMediaIsSearchMediaMySQL` (a library built to trip it - crowds at one
  second, copies at the same second, kept out, of another type - checked row for row against
  `SearchMedia`/`SearchMediaNewerThan`, down, from jumps and up, 0, 1, 5 and 1000 at a time),
  `TestFilteredSearchesByKeysetAndListMySQL` (`searchesLibrary`: that library with tags whose scores
  tie, people twice in a photo and faces taken off theirs, collections, a rated one with ratings
  outside it, positions in clusters, across the antimeridian and without one; 21 filter mixes and
  tags going up, images only or not, three kept-out sets, down, from four jumps and up from them, by
  keyset 0/1/5/1000 and through `DatedMedia` 1/5/1000 at a time, against the whole search row for
  row), `TestRankedSearchesThroughTheirListMySQL` (13 ranked mixes, 1/2/5/1000),
  `TestEstimateMediaMySQL`, `TestMonthCountsKeptAsTheLibraryChangesMySQL` (300 random writes of every
  kind, a folder kept out every 100, both kinds checked against the old query after every 25),
  `TestMonthCountsLeaseMySQL`, `TestMonthCountsWithoutTheirColumnsMySQL`,
  `TestFilteredMonthCountsMySQL`; files_manager's `keysetScenarios` (every `newSearchTest` case in
  every kind over `fakeBrowse` - `TestSearchScenariosKeyset` - and over MariaDB - `...KeysetMySQL`
  -, with `browseChunk` 1 and 240, for the plain browse and with every filter (`scenarioFilter`:
  tag, person, collection and area by keyset and by list - `cListMax` 0 and past the case -, by
  stars and a text; dated so its pages are the case's, and a photo without the filter between the
  first two that a page that didn't filter would show); not the compaction case, which checks a whole
  search's token: `TestKeysetTokenHoldsLittle` does), `TestKeysetFetchThatFails`,
  `TestImageSearchNewerAndJumpSplitTheLibraryKeyset(MySQL)`, `TestImageSearchKeysetLargeMySQL` and
  `TestImageSearchesLargeMySQL`; updater's `TestMonthCountsSchemaIsTheSameInInstallRelease168AndDBSQL`.
  The large ones (`OTC_TEST_BROWSE_LARGE=1`, run with `-p 1`: two at once filled the VM's /tmp) have
  200k photos over fifteen years, 20k synced back at the top, 30k documents, and (the searches')
  tags, people, collections, positions, a rated shoot and Smart search vectors: Lima VM, MariaDB
  11.8, a first page of 12 (before -> now; allocated, kept by the token): the plain browse 0.6-1.7
  s, 142 MB, 82 MB -> 49-58 ms, 0.1 MB, 10 KB; a tag on 40% 281 -> 58 ms (72 -> 22 MB, 35 -> 8 MB);
  a tag on 3% 24 -> 12 ms; two tags 72 -> 14 ms; a person in 10% 68 -> 74 ms (14 -> 5 MB, 9 -> 2
  MB); a person in 0.1% 4 -> 6 ms; a collection of 10% imported years down 43 -> 55 ms (9 -> 2 MB
  kept); areas 0.95-1.7 s -> 24-62 ms (an area of 45%: 61 -> 0.1 MB, 35 MB -> 10 KB); by stars 10 ->
  6 ms; a text over the library 1.06 s, 163 MB -> 17 ms, 14 MB (0.14 MB kept); a text with a tag
  273 -> 71 ms; a person and a tag 492 -> 98 ms; a folder of 20k 45 -> 13 ms; every walk (12 then
  30s, and 7s) identical, thumbnails, copies and all. Month counts 208 ms -> 0.4 ms (built once in
  0.6 s, a read after 100 uploads 2.5 ms); a person's 98 -> 78 ms, an area's 648 -> 257 ms.
  BrowseMedia's first chunk 48 ms (the 20k copies at the top read on the way; ~1 ms after a jump or
  going up from one), the whole walk 2.7 s in chunks of 120, identical to `SearchMedia`.
- Places, the map in Images (2026-10-29, device side; release 148's `updates/148.sh`):
  - The switch (owner, 2026-10-29: "Since we are extracting the coordinates from the Metadata and
    leaving them unencrypted, this feature should be behind a feature flag in the settings").
    `settings.places_enabled` was off by default from release 148; the owner turned it on by default
    on 2026-11-05 ("places should be on by default"): release 170's `updates/170.sh` turns every
    library on once and makes the column default to on - the default is the mark, as 152's for Smart
    search, so a library turned off after that stays off (install.sh runs the same statements,
    `updater/release170_test.go`). Each user turns it off or on for their own library
    (`SetPlacesEnabled` = 148, Ack; `Settings.places_enabled` 8, `optional`: absent = a device
    before Places). `files_manager/places_switch.go`:
    - Off, nothing is kept: `storeLocation` and the backfill write only through `placesWrite`,
      which checks the switch under `placesSwitch.mu`'s read lock; `GetPhotoPlaces` answers
      `error_code "places_off"` (`ErrPlacesOff`, `dev.files.places_off`); an area holds nothing
      (`placesArea`).
    - Turning it off takes the write lock, so it waits for a write under way, and in one transaction
      clears the switch and `locations_backfilled` and deletes every row (`dao.SetPlacesEnabled`);
      `places.reset()` drops the index and masks (an index read before is never put back:
      `floor`). A failed turn off changes nothing.
    - Turning it on starts `BackfillLocations` at once with the request's session. One runs at a
      time (`backfillMu`); `turn` counts the turns, and a run of an earlier one stops at its next
      photo and writes nothing more. `PhotoPlaces.reading` (5) is on and not all read, until this
      turn's run ends (one that couldn't read some ends too: those wait for the next start). The
      run's end drops the index (`places.reset()`) and `PhotoPlaces` reads `reading` before the
      index, so the first answer that says it is done comes from an index read after the last
      position - the apps keep a done answer 5 minutes (seen live: without it the first one held
      76 of 348).
    - Read once from the database (`loadPlaces`); a database before the column reads as off.
    - A demo device refuses the switch; `otc-demo-setup` turns it on (`-places-only` for a demo
      set up before Places, with demo mode off).
    - The city and country search tags (issue #42, `locationTags`, in `file_tags`) don't depend
      on it: they are written as before.
    - Tests: `places_switch_test.go` (read once, off keeps nothing, off deletes, a failed off, off
      waits for a write under way, on starts the backfill), dao's `TestPlacesSwitchMySQL`,
      websocket's `TestPlacesOffRequests`.
  - Storage. `photo_locations` keeps where each photo or video was taken, by content hash (`lat`
    and `lon` in degrees). A row with NULL in both is content that was read and has no position.
    Rows are written while Places is on, by `processMedia` from the metadata it already reads, in
    either lane, after the thumbnail, so Reprocess refreshes them. A row goes with the content's last path or kept
    version (`removeBlobIfUnused`, `dropIfOrphaned`).
  - Backfill. `BackfillLocations` (`location_backfill.go`) fills in what was stored before Places
    was turned on, once per turn on: it runs after `FitStoredThumbnails` in websocket's
    `startBackfillOnce`, because it needs the key, and when Places is turned on, and sets
    `settings.locations_backfilled` when done.
    - It reads only content that has a thumbnail and no row (`MediaWithoutLocation`), and only the
      metadata: a photo up to its EXIF (`exifOfBlob`), a video by ffprobe on its stream - each read
      the processing guard's job (`jobMetadata`, as the dates' pass), set-aside content passed over.
    - It pauses 20 ms after each photo and 250 ms after each video.
    - A missing blob is skipped. A read that failed (a blob error, ffprobe not running or not
      answering) leaves the flag unset, so the next start reads it again. A row is never re-read,
      the NULL ones included.
    - Without the table or the column (release 148's script hasn't run) the device reads that
      nothing has a position (`ErrSchemaBehind`, `isNoSuchTable`).
  - `GetPhotoPlaces` (req 147, answer `PhotoPlaces` 72). It takes a zoom (0-20, clamped) and up to
    64 slippy-map tiles (`PlaceTile`; x modulo 2^zoom, a y off the map dropped), plus SearchPhotos'
    filters.
    - Each tile is 4x4 cells and each non-empty cell is one `PhotoPlace`. A place carries its
      count, position (its photo nearest the members' mean on the Mercator square: the mean
      itself can be where none was taken - Paris and Barcelona at zoom 2 put it near Lyon),
      members' bounding box, its tile (normalised x) and a cover: the
      newest member by the search's date, at the copy the search shows (`ShownCopies`), with no
      content. Its thumbnail comes from `GetThumbnails`.
    - The answer also carries `located_total` and `located_area` for the whole filtered library.
      That area may cross the antimeridian (west > east, `placesExtent`).
  - The index (`files_manager/places.go`). The places come from an in-memory index, about 72 bytes
    a photo, sorted by hash, with Web Mercator positions in 1/2^32ths (`mercator`, `placeEntry`).
    - It is built at the first request from `EachLocatedCopy`. That query is ungrouped, because
      MariaDB took 550 ms to group 100k rows against 115 ms to send them, so the copies are
      grouped by hash in Go.
    - It is rebuilt in the background after `places.changed()`, at most every 15 s; answers in
      between come from the old index. `places.changed()` is called on an upload or link
      (deferred), a delete, a keep-out reload and every position written.
    - It is also rebuilt after 10 minutes in any case, and dropped after 15 idle minutes.
    - Filters keep the index's photos among `EachMatchingMediaHash`'s, the search's own clauses.
      That keep list is held a minute, up to 4 filters.
    - Without filters, `include_videos` false is images only, as browsing.
    - Grouping 100k synthetic photos takes 0.4-1.6 ms on an M-series Mac
      (`TestPlacesGroupsTiming`).
  - Area filter. `SearchPhotos.area` (12) and `ReqPhotoDateBuckets.area` (5) take a `PlaceArea`.
    It is a join on `photo_locations` after the collection's in `searchMediaClauses`, with
    inclusive bounds, across the antimeridian when west > east. It combines with the other
    filters, and the images-only browsing rule still applies.
    - `ImageSearchWithin`, `PhotoDateBucketsWithin` and the dao's `...Within` take it; the old
      names pass nil. `mediaArea` turns a NaN bound into an empty area.
    - A place's area with the same filters gives exactly its photos, because the cell boxes don't
      overlap.
    - Devices before Places ignore the field, so clients send it only to a device that answered
      `GetPhotoPlaces`, and only while Places is on.
  - Tests:
    - `places_test.go`: the tile formula at zoom 0-20 against the slippy formula, the poles and the
      antimeridian, grouping, the cache's rebuild and masks, the answer over sqlmock, timing.
    - `location_backfill_test.go`.
    - dao's `TestPhotoLocationsMySQL`, `TestSearchMediaWithinAreaMySQL`,
      `TestPhotoLocationsWithoutTableMySQL` and `TestEachLocatedCopyLargeMySQL` (with
      `OTC_TEST_PLACES_LARGE=1`, 100k rows).
    - websocket's `TestPlacesRequests`, in a child process.
- On this day (Memories, 2026-10-30, device side; release 150's `updates/150.sh`): the photos
  taken around today in earlier years, the best of each year chosen by the device.
  - Protocol: `GetMemories` (req 149: `date` "YYYY-MM-DD" the client's today, `period`
    `MemoryPeriod` MemoryDay/MemoryWeek/MemoryMonth, `time_zone` IANA name, `utc_offset_minutes`
    when the zone is "" or unknown) answers `Memories` (resp 73: `memories`, `measuring`,
    `hidden_count`); each `Memory` has `key` ("day:2023-10-30", "week:2023-10-27", "month:2023-10"),
    `years_ago`, `start_date`/`end_date`, `place`, `people` (named, faces on only), `cover` and
    `files` (Files without content, in the order taken; thumbnails by path through GetThumbnails)
    and `photo_count`. `HideMemory` (150: `kind` `MemoryHideKind` photo hash / day "YYYY-MM-DD" /
    memory key / person id, `value`, `show` to undo, `show_all`) and `SetMemoriesEnabled` (151)
    answer Ack. `Settings.memories_enabled` (9, `optional`, on by default). Off: `error_code`
    "memories_off" (`dev.files.memories_off`); a bad date or period `dev.files.memories_bad_date`, a
    hide naming nothing `dev.files.memories_bad_hide`. The demo allows GetMemories and refuses the
    two others. Older devices answer `unknown_payload`.
  - Measures (`files_manager/quality.go`, table `photo_quality` by content hash, unencrypted numbers
    like `file_tags`' scores): NIMA's aesthetic score (`images_quality`, below; NULL without the
    model), Laplacian sharpness, clipped dark/bright shares and mean luma, the big thumbnail's size,
    and whether the EXIF names a camera - all from the big thumbnail (<= 1000 px), never the
    original, so the lane and the backfill measure the same pixels. The slow lane measures after
    tags and faces (`storeQuality`); `BackfillQuality` (after `BackfillLocations` in
    `startBackfillOnce`, and when the switch goes on) measures what has no row - or, once the model
    is there, a row without its score - behind `waitForQuiet`, `beginProcessing(jobThumbFix)`,
    resumable, kept-out content never listed (`PhotosToScore`). Rows go with the content
    (`removeBlobIfUnused`, `dropIfOrphaned`). Off measures nothing and deletes nothing.
  - Choice (`files_manager/memories.go`; weights in `memoryWeights`/`memoryTagWeights`, limits as
    constants): candidates are Images' photos (images only, outside kept-out folders) whose OLDEST
    copy falls on the UTC month-days around the windows (`MemoryCandidates`: the copies on those
    days, then each one's oldest copy - a photo synced back on that day years later never counts),
    kept in the client's zone: day = the same day (29 Feb shows on 28 Feb in other years; a 28 Feb
    of a year without a 29th also shows a leap year's 29th), week = ±3 days (a week around New Year
    belongs to the year of its middle day), month = the calendar month. Left out (`leftOut`): what
    the user hid (photo, day, person), screenshot names, document tags (weak ones like "text" only
    without a camera or people) or Smart search's vectors saying the same without tags
    (`smart_documents.go`, release 158: the best of 15 document/screen prompts' cosine less the best
    of 16 moment prompts', >= 0.025 always, >= 0 without a camera or people; the prompts' vectors
    made once per process by the text tower; calibrated on the demo's 114 photos, all <= -0.023,
    against 41 printed pages, screenshots and pages/receipts/phones laid on tables, the frame-filling
    ones >= +0.02; on a test device with tagging off, release 157 made memories of 21 such
    pictures dated on one day, 158 of 1, a small phone in a table scene), PNG/GIF/WebP and very tall pictures without a camera, sharpness
    under 15, > 60% clipped or mean luma outside 20-240, under 400 px. Per year: moments split at 2 h
    or a city change; score = people (named, groups; faces on only) + collection + own posts with
    likes/comments + a trip (city/country other than the library's commonest place tags) + tags +
    moment size + NIMA/sharpness/clipping; picks by score, at most a share per moment, a burst once
    (3 s, or 2 min and a 64-bit dHash within 10 bits, from the small thumbnail, never stored); day 8
    picks (2 needed, or 1 scoring >= 2), week 10, month 12 (3 needed); in the order taken, cover the
    best; most recent year first. Answers kept per (date, period, zone, faces) for the device's day
    until anything changes them (`memoriesState.changed`: a delete, a measure, a hide, the switch,
    keep-out folders, people fixed). 100k photos: the day's query 15 ms, a month's 57 ms (Lima VM).
  - Tests: `memories_test.go` (windows, years, month-days, keys, filters, choice, bursts, moments,
    switch), `quality_test.go`, dao's `TestMemoryCandidatesMySQL`, `TestPhotosToScoreMySQL`,
    `TestMemoriesSignalsMySQL`, `TestMemoriesWithoutTheirTablesMySQL`,
    `TestMemoryCandidatesLargeMySQL` (`OTC_TEST_MEMORIES_LARGE=1`), websocket's
    `TestMemoriesRequests`, modelserver's quality tests.
  - With Smart search on, bursts are told apart by its vectors instead (`memoryOptions.similar`,
    below): cosine >= 0.95 is the same photo whenever taken, >= 0.92 within 60 s a burst; a photo
    without a vector falls back to the dHash rule.
- Smart search (beta, 2026-10-30, device side; release 151's `updates/151.sh`; since release 152
  on by default and its model on demand): photos found by describing them, in any of the seven
  languages, with Google's SigLIP 2 base (`images_embedder`, below; files from
  `models/siglip2_export.py`).
  - On by default (the owner, 2026-10-30; 151 had it off): db.sql's `smart_search_enabled`
    defaults to 1, so a fresh install and a new user (provisioning loads the embedded db.sql) start
    with it on; `updates/152.sh` (and install.sh's migration block, which the container runs) turns
    every library on once and sets the default - the column's default is the mark, so a run again
    never turns back on a library its user turned off. A device that updates downloads the model at
    start (`resumeSmartModel`) and describes the photos after the next sign-in (it needs the key).
    On a 4 GB device that is the download (415 MB on the SD card, at the cap), then the image
    tower while the backfill runs (one call at a time with the other models, NoArena, 2 threads,
    released after 10 idle minutes; the process held ~240 MB more with both towers loaded in the
    Lima VM's low-memory mode), resting as long as each photo took.
  - Protocol: `Settings.smart_search_enabled` (10, `optional`, on by default since 152) and
    `SetSmartSearchEnabled` (req 152) -> Ack: on starts `BackfillEmbeddings` with the session -
    or, without the model, starts its download and answers Ack at once (below); off deletes
    every vector in one transaction (`dao.SetSmartSearchEnabled`), drops the decrypted ones and
    stops the download unless another instance wants it. `Settings.smart_search_model` (11,
    `SmartSearchModel{ready, downloading, percent, problem}`, always set: `SmartSearchModel(lang)`;
    a user's instance from the primary's "info"). `SearchPhotos.text` (13): the photos the other filters keep (tags, people,
    collection, area - exact as always), ranked by cosine to the text, best first, cut at
    `cos >= max(0.05, 0.7 x best)` (`rankScores`; demo library: precision 0.84, recall 0.87), paged
    through the same tokens as a tag search; `before`/`newer_than` don't apply (a text search is
    never `oldest_first`). `ListOfFiles.smart_search_reading` (7): a text search while the backfill
    of this turn hasn't finished. Codes: `smart_search_off` (`dev.files.smart_search_off`, a text
    search - even a token's next page - while off), `smart_search_downloading`
    (`dev.files.smart_search_downloading` with the percent, a text search while the model is on its
    way: `ErrSmartSearchDownloading`, checked before the database is asked) and
    `smart_search_unavailable` (`dev.files.smart_search_unavailable`, turning it on or searching on a
    device that can't get the model at all: nowhere to download it, or a user's instance whose
    primary is older than the download on demand and hasn't it). Clients send `text` only while
    Settings says on: older devices ignore it and answer the whole library.
    The demo allows the search and refuses the switch (`otc-demo-setup` turns it on when the
    device has the model; `-smart-search-only`).
  - Storage: `photo_embeddings` (hash, `model` = `cEmbedModel`, `vec` sealed with
    `session.Encrypt`: format byte, float32 scale, one int8 a number - 773 bytes, 801 sealed - and
    `made`). Unlike `file_tags`, sealed: a vector says much about what a photo shows. Written by the
    slow lane after `storeQuality` (`storeEmbedding`, images only) and by `BackfillEmbeddings`
    (after `BackfillQuality` in `startBackfillOnce`, and when turned on; `PhotosToEmbed`: no row,
    or another model's; kept-out content never; `waitForQuiet`, `beginProcessing(jobThumbFix)`,
    rests as long as each photo took, at least 30 ms; resumable; `reading` cleared only by a run of
    this turn that described everything). Every write goes through `smartWrite` under the switch's
    read lock (Places' pattern), so none lands after off. From the big thumbnail, never the
    original. Rows go with the content (`forgetEmbedding` in `removeBlobIfUnused`/`dropIfOrphaned`).
  - Search: the vectors are decrypted into memory at the first text search or burst check
    (`smartIndex`, ~0.8 KB a photo, dropped after 15 idle minutes, on off, and per deleted hash)
    and scanned brute force (int8 x float32: 50k photos ~9 ms on an M-series Mac). The query goes
    through the text tower (lowercased, 64 Gemma tokens); text is trimmed and cut at 1000 bytes.
  - Model: five files, `image.onnx` + `image.onnx.data`, `text.onnx` + `text.onnx.data`,
    `tokenizer.model`, 414,935,872 bytes (`smartModelFiles` pins each one's size and SHA-256, the
    `models` release's assets `siglip2-base-*`). Release 151 had update.sh, install.sh and the
    container's entrypoint fetch them on every device at every update, Smart search on or off: on a
    slow home link that took the whole connection for a long while. Since release 152 nothing fetches them but the device, and only once someone turns Smart search
    on (`files_manager/smart_model.go`, `modelfetch`):
    - Where: `[embedder]`'s `image-model-path`, `text-model-path`, `tokenizer-path`
      (`/usr/local/models/siglip2-base/`; the container's `$MODELS/siglip2-base`) are used where
      the files are - each tower's .onnx with its .data beside it (the .onnx names it), the
      tokenizer apart - so a device that got them from release 151, and the demo, keeps them. What
      isn't there is downloaded into
      `[embedder] download-dir`, by default `models/siglip2-base` in the primary's working
      directory (`/var/lib/otc/models/siglip2-base` on a Pi: the service runs `ProtectSystem=full`
      and can't write `/usr`; the container sets it to `$MODELS/siglip2-base` on the state volume, so
      a container made again finds them). `[embedder] model-url` (a fork's release) and
      `download-kbps` (kilobytes a second: 1024 by default, 0 no cap).
    - `modelfetch.Fetcher` (primary only): plain Go HTTP (30 s to connect, a minute for the
      answer's headers, cut after 2 minutes without a byte - never a limit on the whole transfer),
      each file into `.<name>.part` in the directory, hashed while written, renamed only when it
      matches its pin; a part is resumed with `Range` (a 200 starts it again); the speed cap waits
      after each 32 KB read (no credit while the link is slower); free space checked first (the
      files still missing against `freeSpaceReserve`, `smartModelRoom`); a failed try waits 1 min,
      doubling to 1 h (back to 1 min after a try that wrote 1 MB), and stops when nobody wants it
      any more (`Wanted`). Progress = bytes on the disk of what was missing when it was asked
      for. `problem` kinds `no_space` (need, left), `unreachable` (connection, stall, 5xx),
      `failed` (pin mismatch, write error, 4xx), worded `dev.files.smart_search_model_*`.
    - Who wants it (`localModel`): the primary's own switch (`self`) and the users' instances'
      leases - "embed-fetch" over the model socket (`Who` = the user's id, `Want`), renewed every
      minute (`watchSmartModel`) while that instance is on without the model, forgotten 5 minutes
      after the last (`cSmartLease`). On starts or joins the download; off by the last one stops
      it and removes the `.part` files (whole files stay - not anyone's data). At start the primary
      asks for it when its own switch is on (`resumeSmartModel`: a 151 -> 152 update whose 151
      download failed recovers by itself); `GetSettings`, a text search and the users' instances'
      asks while on ask again. No `[embedder]` section is fine (defaults); no working directory and
      no `download-dir` = `smart_search_unavailable`.
    - When it is there (`adoptSmartModel`, from the download's end, or whoever sees it first) the
      model is set atomically (`Manager.embedder`, an `atomic.Pointer`, read through
      `smartEmbedder()`; the model socket asks `Models.Embedder()` per request) and, while on, the
      photos are described with the session that turned it on (`smartWait`: same turn, same
      password generation), otherwise at the next sign-in (`SmartSearchSignedIn`, from
      `startBackfillOnce` at every sign-in). A user's instance learns it from "info"
      (`EmbedderStatus`, `HasEmbedder`) at its next ask, Settings or search.
    - `embedModels` loads each tower at first use and releases both after 10 idle minutes (Smart
      search is off on most devices); one call at a time, with the other models' too on a
      low-memory device (idleModels' `run`, NoArena, 2 threads). The primary serves both towers to
      the users' instances (`modelserver` ops "embed-image" with the 256x256 pixels and
      "embed-text", `HasEmbedder` in "info"; an older primary answers `ErrNoEmbedder`). The weights
      in the `.data` files are memory-mapped by ONNX Runtime: the text tower adds ~130 MB resident
      (~800 MB with them inside the .onnx).
  - Tests: `smart_search_test.go` (packing, cutoff, text, bursts, lazy towers and idle release,
    the switch over sqlmock, ranking with a real session), `smart_model_test.go` (on without the
    model, the session held or the next sign-in, off while downloading, the primary's download
    while any instance wants it against a test server, leases, `[embedder]`'s files where they are,
    a user's instance over a real socket), `modelfetch`'s tests (pins, resume, a cut transfer, the
    cap and the backoff on an injected clock, Stop, free space, a stall), dao's
    `TestPhotoEmbeddingsMySQL` and `TestPhotoEmbeddingsWithoutTheirTablesMySQL`, modelserver's
    `embedder_test.go` ("embed-fetch", an older primary, a child from release 151),
    `images_embedder`'s tests (`OTC_EMBEDDER_DIR`, `OTC_ORT_LIB` off Linux).
- A slideshow's order (2026-11-06; owner: "the order should be random"; `dao/random_media.go`,
  `files_manager/random_photos.go`): `RandomPhotos` (req 161: `group_id` - a collection, "" the whole
  library -, `seed`, `after`, `limit` 1-50 with 20 for 0, `small_thumbnails`) answers `ListOfFiles`:
  the page, `token` the next page's `after` ("" once the round is over), and on a round's first page
  `random_total` (8), the photos it holds. The order is `md5(seed + hash)` with the hash after it
  (`k`): a new shuffle for each seed, the same for every page of one, nothing kept between pages - a
  page is `k > after order by k limit n` over the round's contents (`randomRows`: the library's image
  rows outside the folders kept out, distinct by hash; or a collection's members with such a row, one
  look-up by hash each), then their rows through `SearchMediaOf` (each at its oldest copy, as Images),
  images only, in the round's order, and only processed ones (#147, `gridThumbnailState`, or the small
  thumbnail read for `small_thumbnails`): a page can come short or empty before the round ends. An
  `after` that is no page's key ends the round. A content added mid-round shows if its place is still
  ahead; one deleted doesn't. 50,000 photos: 65-77 ms a page in the Lima VM (a scan of `files` each,
  every 20 photos, i.e. 100 s of show). The demo allows it. Tests: dao's `TestRandomMediaRoundsMySQL`
  (and `...LargeMySQL`, `OTC_TEST_RANDOM_LARGE=1`), files_manager's `TestRandomPhotosPagesMySQL`.
- Background work (release 152; `files_manager/background.go`): `Status.background_tasks` (20,
  `BackgroundTask{kind, text, done, total}`) is what the instance is working through on its own
  library, one entry a kind, in a fixed order (`backgroundOrder`), `text` the device's words in the
  request's language (`dev.status.background.<kind>`). A pass reports into the registry:
  `mg.BeginBackground(kind, total)` (total 0 = not known), `Step(n)`, `AddTotal`/`SetTotal`, `End`
  (deferred; nil-safe); passes of one kind are summed, a total under what is done is raised to it.
  Kinds: "analysis" - the processing lanes' busy spell (`analysisSpell`: from the first file queued
  while they were idle - `queueThumbnail`/`queueAnalysis` in enqueueMedia, enqueueAnalysis and
  ResumePendingAnalysis - total what was queued, done what left them, `mediaLanes.finished`; gone
  when they are idle); "thumbnails" (`BackfillMissingThumbnails` from its first repair, total
  unknown; `FitStoredThumbnails` with `CountMediaFilesAfter`); "places" (`BackfillLocations`,
  `CountMediaWithoutLocation`); "quality" (`BackfillQuality`, `CountPhotosToScore`);
  "smart_search" (`BackfillEmbeddings`, `CountPhotosToEmbed`); "dates" (`BackfillDatesTaken`,
  `CountMediaFilesAfter` from its cursor). The counts are the listings' rows (`dao/background_counts.go`,
  `TestBackgroundCountsMySQL`), asked once when a pass starts; a pass with nothing to do isn't
  listed. A pass waiting for the uploads or the lanes stays listed. Not listed: Reprocess Media
  (`GetReprocessStatus`) and Smart search's model download (bytes, already in
  `Settings.smart_search_model`). Tests: `background_test.go`, websocket's
  `TestStatusListsBackgroundWork`.
- Home network (issue #190): at home the apps reach the device directly over a **pinned TLS**
  connection; anywhere else, or on any failure, through the bridge as before.
  - Device: `lantls` keeps an ECDSA P-256 key and a self-signed cert (CN/SAN `otc-lan`, 100 years)
    in `<storage-path>/.lan-tls/` (0700/0600), created once and NEVER regenerated: the apps hold
    its pin, SHA-256 of the leaf DER. `api/local_tls.go` serves the same mux (`/ws`, `/media/`,
    static) over TLS 1.2+, HTTP/1.1 only, on `[otc-api] lan-tls-port` (default HTTP port + 363:
    8443, or 8444 for a child on 8081). `GetLocalEndpoint` (owner sessions only, in
    `processAuthRequest`) answers `LocalEndpoint{addresses, port, cert_sha256}`. Addresses are
    private IPv4 and ULA on up interfaces, never loopback, link-local, the setup hotspot (uap0 and
    10.42.0.0/24, pinned in `network_setup.py`; change both together), Tailscale or docker. It
    answers `local_unavailable` when there is no listener or address (the apps then forget the
    endpoint) and a bare error until `api.Init` has wired it. Older devices answer
    `unknown_payload`.
  - Apps (iOS `HomeNetwork.swift`, Android `net/HomeNetwork.kt` + `NetworkWatch.kt`, macOS
    `LocalRoute.swift`, otc-sync `wsclient/local.go`): after a sign-in through the configured
    address they ask `GetLocalEndpoint` and keep it bound to that address. Keychain / otc_secrets /
    `<config dir>/local.json` (0600). They keep only RFC 1918 or ULA literals (at most 8), drop it on
    `unknown_payload`/`local_unavailable`, and keep it on any other error. Every connect races all
    addresses as `wss://<addr>:<port>/ws` within 2.5 s, accepting ONLY a leaf certificate whose DER
    SHA-256 equals the pin; then sign-in runs as usual (GetPubKey + the RSA-sealed password: safe
    once the pin matched, and it works with the internet down). Route re-checks happen on a network
    change, the foreground (Mac: the popover, at most every 120 s) and a wake, never while a
    transfer runs (`TransferActivity` / `OTCConnection.transfer {}` / `folderBusy`). A check put
    off is recorded and runs after. Settings shows "Connected over your home network" or
    "Connected through off-the.cloud".
  - A ws:// address on the phones (a device set up without the bridge, or left it): a private
    address or a `.local` name is the home network only while the phone is at home, and on another network
    with the same range whoever held it got the sign-in in the clear, the key the password is
    sealed with included. Its connects race the pinned link with the address's own host added at
    the pinned port (`OTCConnection.raceTargets`, `LocalEndpoint.including`: a `.local` name or a
    Tailscale address where the listed ones don't answer, a container's own addresses). Once this
    phone has signed in over that link (`LocalEndpoint.reached`, set at a home sign-in - the
    route check's move home included - kept while the pin is the same, `keepingReach`, and by
    Leave the Bridge, `LocalEndpoint.move`) the address goes only the pinned way
    (`OTCConnection.pinnedOnly`). Until then a missed race goes on to the address as before, so a
    device whose encrypted port the phone can't reach (a firewall letting only 8080 through, a
    container publishing only 8080) keeps working; a stored endpoint from before has no
    `reached` and counts as not reached. Pinned only, a missed race asks whatever answers at the
    address for GetPubKey alone (`probeInTheClear`, nothing of the phone's in it) and never
    sends it the password: nothing answers - the usual "isn't answering"; `storage_unavailable` -
    issue #207's state in the app's own words (a device without its storage serves no listener,
    and nothing proves it is the device); anything else - the network is up, so the link is
    raced once more with a longer budget (`HomeNetwork.secondRaceBudget`, 10 s: a Wi-Fi just
    waking, a busy device) and signed in over if it answers. Still nothing: a certificate other
    than the pin seen at one of its addresses (`PinnedSession.contest`'s `otherCertificate`) is
    `app.shell.not_known_device` (something else holds the address; a device set up again from
    scratch is reached again through "Set up or connect a different device"), only timeouts and
    refusals are `app.shell.home_port_unreachable` with the port (error 9, no pointer to that
    button: whoever answers can't be told from the device). http:// media goes only while the
    session itself is in the clear (`signedInInClear`: a first sign-in, a device without a pin
    or not reached). Every sign-in over the home link asks GetLocalEndpoint again there
    (`updateOverHome`: new addresses only, the pin never dropped). Saving the same address
    spelled otherwise (a host's case) keeps its pin (`saveCredentials`: `move`), never a sign-in
    in the clear. Tests: `PinnedAddressTests.swift`, `HomeNetworkTests.swift` (the race's
    outcome). Android has the same rules and names (`net/PinnedAddress.kt`: `pinnedOnly`,
    `raceTargets`, `probe`, `find`, `homeAfterSave`; `HomeEndpoint.reached`, stored as a fourth
    part `|reached` of `home_endpoint` - an older three-part value reads as not reached -,
    `including`, `keepingReach`, `signedInOver`; `HomeNetwork.contest`, whose pinned trust manager
    throws `OtherCertificate` for a certificate that isn't the pin; `OTCConnection.connectPinned`,
    `markReached`, `refreshHomeEndpoint(overHome =)`, `signedInInClear` for `MediaStream`). The
    pinned way installs no socket until it has signed in, so its failed connect schedules the
    backoff retry itself (`scheduleRetry`, `PinnedAddress.retriesAfter`: not after a password
    turned down, the storage answer or new settings), as a failed connect through the endpoint
    does by its socket's drop (on iOS the probe dials the connection's own socket, so a probe
    nothing answers comes back that way). Its
    route check leaving home (`switchRoute`) never dials a pinned address: the home socket stays
    until it drops, and that reconnect goes by the rules above. A name raced on a bound network
    (the home Wi-Fi with mobile data the default) is looked up on that network. Tests:
    `PinnedAddressTest.kt`, `HomeNetworkTest.kt`.
  - Video: `/media/<token>` is relative, so the base follows the route. Android plays through
    ExoPlayer on the pinned OkHttp client; iOS streams local media through an
    `AVAssetResourceLoader` on a custom scheme over the pinned URLSession, and bridge media plays as
    before. Never add a trust-all path outside the pinned clients (Play flags a verifier that
    always returns true). The web can't pin a self-signed key and stays on the bridge (or
    `http://otc.local:8080` at home).
  - Checked live on 2026-10-06 against Pit: endpoint 192.168.50.201:8443; a wrong pin was refused
    before anything was sent; sign-in over the home network took 0.3 s; a 4 MB range of a video
    returned HTTP 206 in 1.1 s.

### Device-side package layout (root Go module)

Flat, one-package-per-concern, wired together in `bin/otc.go`:

- `container` — the Docker image (`docker/`, beta): `OTC_CONTAINER=1`, set by the image only, and a
  no-op everywhere else. Sizes memory from the cgroup (`MemoryTotal`: the limit, `OTC_MEMORY_GB`, 8 GB
  without a limit), checks whether a release's image is on ghcr.io (`ImageLookup`: one token per
  check; "not published" - 401, 403 and 404 included - kept 15 minutes, a registry that failed left
  alone for 5), and holds the first-password rules (`HomeAddress`, `OTC_SETUP_UNTIL` in Unix
  seconds, at most 31 minutes ahead). Callers gate on `container.Enabled()`: `websocket/container.go`
  (`containerGate` beside `demoGate`: no disks or Wi-Fi; a first password only over a direct
  connection from a home address with no proxy headers, no other site's Origin and no page at a
  name that isn't the device's (`indirect`: `deviceHost`, DNS rebinding) -
  the primary's within the window, an additional user's (`OTC_SUPERVISED_CHILD`) at any time or
  through the bridge - and refused when the vault can't be read; `[otc-api] trusted-proxies` for
  X-Forwarded-For; user ports from the HTTP port, checked against the host), the updater
  (image-gated releases, no Apply; a registry that can't be asked marks the check
  `ImageCheckFailed`, which keeps the alert and shows Settings' check error), raidwatch and RAID
  status (off, or the mirror's array in mirror mode, below), files_manager's sizing (`GOMAXPROCS`,
  ffmpeg threads), fatal listeners for the primary (`listenersFatal`; a user's instance logs a busy
  port and keeps serving through the bridge) and no `:80` in `api`, extra interface skips in `lantls`.
  `PubKey.container` and `RespUpdateInfo.container` tell the clients. Publishing is local, never
  GitHub Actions (the owner, 2026-10-26: Actions cost too much): `scripts/docker-release.sh
  [--no-publish] [--arch amd64|arm64] [N]` on the Mac, after `release.sh`. It verifies
  `VERSIONS.sig`, builds from the release's own `src.tar.gz` + `web-dist.tar.gz` (hashes checked)
  with `docker/docker-bake.hcl` (the build file, test and client from HEAD by `git archive`;
  `APT_REFRESH`, the release and the day, set once a run, rebuilds `otc-base`'s package layer with
  `apt-get upgrade` - BuildKit kept it, and Debian's fixes out of every image, for as long as the
  base tag stayed; a container never upgrades its packages itself, docs/docker.md says so), arm64
  natively and amd64 under Rosetta (which shows amd64 containers an x86 `/proc/cpuinfo`, SSE4.1
  included), runs `docker/smoke/test-image.sh` on each (healthy, version, web app, set-password,
  `docker/smoke` sign-in client, restart, no panic), then `test-image.sh --mirror` (the same on two
  blank 3 GiB loop disks made by a privileged helper, plus the mount, `/proc/mdstat` and a down/up
  reassembly; a release without `docker/mirror` is refused), pushes each by digest only after both
  passed and checks its layers against the tested image, then tags `:<N>` (and `:beta`/`:latest` for
  the newest release); a release already on ghcr.io is left alone. It all runs in a Colima VM of its
  own (profile `otc-image`, 8 CPUs/12 GB/80 GB, `--vz-rosetta`) with its own `DOCKER_CONFIG`
  (`~/.otc/image-build/docker`) and a docker-container buildx builder (`otc-image`) whose cache keeps
  the toolchain and OpenCV between releases; the default Colima VM, its containers and `~/.docker`
  are never touched. The VM is stopped at the end if the script started it. Publishing needs gh's
  token with `write:packages` (`gh auth refresh -h github.com -s write:packages`, the owner's to
  run) and logs out of ghcr.io at the end. The test script runs on any Linux Docker host.
  The entrypoint makes the nightly database copy (03:00) in the background, in a process group of
  its own (`nightly_dump`: `otc-entrypoint dump` again; in the foreground it held the supervise loop -
  a device that stopped wasn't started again, and a `docker stop` waited until Docker killed
  MariaDB), stopped first by `stop_all` (`stop_dump`), one copy at a time (`flock` on
  `/run/otc-dump.lock`, so a `docker exec ... dump` meanwhile is refused). In `run`, a `die` waits
  before the container exits (`start_backoff`: Docker's `unless-stopped` starts a container that ran
  10 s again at once, so a failure that stays - a migration, MariaDB, a busy port - was a tight
  loop of MariaDB crash recoveries; 30 s doubling with each start that failed in a row up to 30
  minutes, an hour at least after a model download - `download_failed` -, counted in root's
  `$CONF/start-failures` and forgotten once the device has run 10 minutes, `start_succeeded`; a stop
  signal ends the wait). `bash docker/entrypoint-test.sh` tests both on any Linux host, without
  Docker or root (a copy with its paths in a temporary directory, MariaDB's tools stood in for).
  Mirror mode (opt-in: `docker/compose.mirror.yaml` through `COMPOSE_FILE`, which sets
  `OTC_MIRROR=1`): `.env` names two whole disks (`OTC_MIRROR_DISK1/2`, by-id paths Docker maps to
  `/dev/otc-disk1/2`; their basenames are the labels) and, by the same strings, any that may be wiped
  when not blank (`OTC_MIRROR_ERASE`). `otc-mirror` (`docker/mirror`, pure Go, root,
  `/usr/local/sbin`) `prepare`s the RAID1 - adopts an active array on them, assembles their
  superblocks, or builds one only on free disks that are blank or listed, never touching another
  array's - and mounts it on `/mnt/storage` over the empty host folder. The entrypoint runs it before
  preflight (`--no-create` for restore), reads its `key=value` stdout without eval and dies when it
  fails (never falling back to the folder; a `/mnt/storage/mysql` volume is refused, the mirror would
  hide it), supervises `watch --array` (state, rebuild progress and the Pi's grow, in
  `/var/log/otc-storage/storage.log`) and runs `release` after MariaDB in `stop_all`. Only the
  device's environment gets `OTC_MIRROR_ARRAY`, `OTC_MIRROR_DEV1/2` (major:minor) and
  `OTC_MIRROR_LABEL1/2` (`container.MirrorArray`/`MirrorDisks`); otc-mirror itself takes
  `OTC_MIRROR_DEV1/2` as device paths (a test override), so the entrypoint never exports them. The
  override adds the two devices, `device_cgroup_rules`
  `b 9:* rmw`, `SYS_ADMIN` + `MKNOD` + `SYS_RESOURCE` (online `resize2fs`) and AppArmor/SELinux off
  (Docker's AppArmor profile refuses mount; the devices in the long `source`/`target` form, as USB
  disks' by-id names hold a `:`): root in that container is close to root on the host, while the
  device still runs with no capabilities - and, in either mode, under a seccomp filter of its own:
  with `SYS_ADMIN` in the container, Docker's default profile allows every process in it `unshare`,
  `setns`, clone with `CLONE_NEW*`, `mount` and `bpf` (it decides per container, by capability),
  so code run in the device could take capabilities in a user namespace of its own (the nf_tables
  kind of kernel bug). `otc-sandbox` (`docker/sandbox`, static, `/usr/local/sbin`; the entrypoint's
  `start_app` runs `setpriv` through it; MariaDB, which listens on the host's 127.0.0.1 with
  `network_mode: host`, `mariadb-install-db`, the log's tail and every `as_otc` helper - the models'
  `curl` among them - run through it as well, once `probe_sandbox` found its `--probe` answering ok,
  so no unprivileged process in the container is left outside) installs a classic-BPF filter
  refusing what that profile refuses a container without `SYS_ADMIN` - those, the newer mount
  calls, `perf_event_open`, `quotactl`, `syslog`, `sethostname`, and the keyring, file handles,
  module loading and `pivot_root` - with clone's flags masked as Docker does and `clone3` answering
  ENOSYS (glibc falls back to clone); calls of a foreign architecture and x32 are refused, and both
  architectures' numbers are in the one program (`filter.go`; numbers checked against x/sys by
  `tables_linux_test.go`). It sets no_new_privs, installs with `SECCOMP_FILTER_FLAG_TSYNC` and
  execs the command; first a copy of itself (`--probe`) installs it and checks it - where it can't
  be (Rosetta answers `seccomp()` EINVAL, so `docker-release.sh`'s amd64 tests run without it) the
  device runs without, saying so (and MariaDB and the helpers then run without it too, with no
  warning each). `test-image.sh` checks the device and `mariadbd` have one filter more than the
  container's PID 1 and, in mirror mode, that a process run as the device's gets no user
  namespace; `entrypoint-test.sh` checks what runs through it. Tests: `filter_test.go` (a BPF
  interpreter: every call of both tables, clone's flags, clone3, foreign architectures, x32),
  `sandbox_linux_test.go` (real calls under the filter: EPERM,
  ENOSYS, and exec, glibc threads and pipes still working), run in the Linux VM and in containers
  with the mirror's capabilities. Rules otc-mirror keeps (each from a review or the VM):
  only arrays named `otc`/`*:otc` are used (another system's raid1 never), and once
  `config/storage-mode` names this device's mirror (`OTC_MIRROR_UUID`) only that one; `mkfs.ext4`
  only on an array built in that run, never on one whose filesystem can't be read (a read error on a
  degraded mirror hides it from blkid and wipefs); a disk is "blank" only if its first and last MiB
  read back; `OTC_MIRROR_ERASE` only by `/dev/disk/by-id/` name; a former member is re-added only if
  its superblock's Array State doesn't show it ran without a disk the mirror runs on now (split
  copies are left for the owner); a faulty member doesn't make the array foreign; `mdadm --grow`
  runs under `unshare --mount` on a private read-write sysfs (Docker's `/sys` is read-only); a disk
  may be `/dev/null` in `.env` to run on the other alone (never to build). The entrypoint's
  `config/storage-mode` (`folder` or `mirror <uuid>`) refuses a start on the other storage.
- `cfg` — INI config loader (`etc/otc_<env>.ini`, falling back to `/etc/otc_<env>.ini`); `env` is
  `os.Args[1]` (defaults to `"dev"`). All other packages pull settings via `cfg.GetStr/GetInt/...`.
- `dao` — the only package that talks to MySQL/MariaDB directly (schema in `db/db.sql`: `files`,
  `file_tags`, `social_publications` + likes/comments, `social_friendship`, `settings`, `profile`,
  `shared_links`, `vault`, `events`, `people`, `faces`, `image_groups` + `image_group_files`,
  `upload_only_folders` + `file_versions`, `out_of_images_folders` + `skipped_analysis`, `notifications`). Business logic in other packages should go
  through `dao`, not raw SQL. Issue #64: a device-side error the owner should know about (a
  photo that could not be processed, an upload that never reached the disk) goes into
  `notifications` as type `Error` through `dao.AddErrorNotification(alerttext.Alert)` (the alert
  writers are in `dao/notifications.go`) - grouped, so an error
  within five minutes of an open Error row joins it (`details` gains a line, `occurrences`
  goes up, the row is unread again) rather than adding a row - never a standalone row
  (`actor_domain` `(standalone)`: `UpsertErrorNotification`'s memory and set-aside Alerts);
  `files_manager.alert(what, path, err)` is the one call site helper (`what` an
  `i18n.DevAlertFile*` constructor, the error's own words the raw detail). Every alert is an
  `alerttext.Alert` (`alerttext.New`/`WithRaw`, never a literal; a new one needs a `dev.alert.*`
  key with `"stored": true` and a row in `everyAlertKey()`): `title`/`details` get the English as
  before, `msg_key`/`msg_args`/`msg_lines` the keys, which `ListNotifications` renders in the
  request's language (`dao.LocalizeNotifications`; docs/i18n.md "Alerts"). Not alerted, only
  logged (`files_manager/processing_alerts.go`): an image/*
  file in a format the device makes no preview of that fails to decode (a `.cur`, a `.djvu`: no
  thumbnail, no analysis; "previewed" is `previewedMimes` - the Go decoders, HEIC/HEIF, and JPEG
  2000/Photoshop through ffmpeg - by a `.HEIC` name, the row's MIME or the sniffed content, so a
  damaged JPEG or HEIC is still alerted, even one stored as application/octet-stream), and any
  processing failure (decode, HEIC conversion, video frames, thumbnail encode, a recovered panic:
  `processingAlert`, `safelyOn`) of content kept out of Images (#192's rule, `keptOutOfImages`),
  logged by hash at Info and by path at Debug only (#156); disk read/write failures are alerted
  wherever the file is. HEIC's HEVC stills and grids are decoded by `decodeHeic` (heif_decode.go),
  not goheif's own decoder, which read 10-bit samples (phones' HDR photos) as 8-bit and panicked
  placing a 10-bit grid's tiles (`TestDecodeHeicGridAtTenBits`); AV1 still goes to goheif. A camera
  raw (a DNG - Apple ProRAW, whose IFD0 x/image/tiff refused as "color model" - or a TIFF with a
  CFA/linear-raw directory) is its largest embedded 8-bit JPEG (`rawPreview`, raw_preview.go), first;
  any other TIFF Go can't read is tried for one before ffmpeg. Its parse reads only the values it
  looks at (`maxValues`: a scalar's first, 16 bits-per-sample, 2 strip entries - one strip or more -,
  64 sub-image offsets, 64 KiB of JPEG tables) and 256K values in all (`cMaxRawValues`), and puts
  together only the largest preview (a strip with its tables is a copy): a crafted 1 MB TIFF of 64
  directories, every tag a million values on the same bytes, held 3.4 GB before the decode budget
  was asked for anything (`TestCraftedRawIsCheapToRead`). A preview
  that failed leaves `<hash>.nothumb` holding `cDecoders`: `BackfillMissingThumbnails` skips it
  until a release adds to `cDecoders` (heic10 did), then retries it once. The clients show one line per row and the full list on hover (web)
  or tap (iOS/Android). Never push-notify these - the one exception is `raidwatch`'s disk failure below. `AddErrorNotification` is serialised in-process,
  and a group's `details` stop growing at 60000 bytes (later errors still count). The device DSN
  has `interpolateParams=true` (the provisioning DSNs in `dao/provisioning.go` don't).
  Release 143 dropped `files`' indexes on `modified` and `size` (no query filters or sorts by
  either); no `(created, path)` index can exist - `path` alone is InnoDB's 3072-byte key limit
  (ERROR 1071 on 11.8), and so the `created` index isn't extended by it either (with no PRIMARY
  KEY, MariaDB never extends a key by the unique one): Images' plain browse goes by `created` and
  whole seconds instead (issue #206). MariaDB's buffer
  pool goes by the memory: 256 MB with 4 GB, 768 MB with 8 GB, 1 GB from 12 GB, the default under 3
  GB - `/etc/mysql/mariadb.conf.d/70-otc-memory.cnf` from install.sh and 143.sh (from MariaDB's
  next start: update.sh keeps otc and its users' instances running through the migrations, so it
  restarts nothing, and 11.8 can't grow the pool past its starting size), and
  `--innodb-buffer-pool-size` from the container's entrypoint (`buffer_pool_mb`, the memory as
  `container.MemorySizing` reads it).
- `files_manager` — file storage, hashing, dedup on disk. Content is keyed by hash, and the
  hash-first upload (`HasFile`/`HasCloudIds` then `LinkFile`) only skips the bytes when the blob is
  really on the disk (`hasBlob`: present and non-empty; each answer is recorded in
  `missingBlobs`, which is what listings use instead of a stat per file - issue #173; folder
  listings find rows with `dao.underPrefix`, an index range plus an escaped LIKE, never a
  REGEXP) - a database row alone once made a device
  claim content whose blob was gone, so no client ever re-sent it and every `GetFile` came back
  empty; `GetFile` now returns the read/decrypt error (and raises a #64 alert) instead of a File
  with no content. The sync clients verify a download's hash before writing it, for the same
  reason. Issue #141: a blob is only ever removed or relied on under its hash's
  lock (`lockBlob`, 256 stripes): `removeBlobIfUnused` checks-and-removes, `withBlob` checks-and-
  stores a row, and UploadFile's background write holds it too - never across a `DelFile`, which
  takes its own. Blobs are written to a temporary file and renamed into place (`writeBlob`):
no 0-byte blob after a crash, and a blob owned by another account (a recovered older
installation's `pi`) can still be replaced; release 22's script hands such files to `otc`. The
folder is synced after each rename too (`blobstore.SyncDir`: `Commit`, `writeBlob`, social's
`writeFileAtomic`, the posts' copies), so a power cut can't take a name back while its row stays.
**Paths** (2026-11, `paths.go`): a path reaches `files` only through `registerUpload` (UploadFile,
FinishUpload) or `LinkFile` - nothing renames, moves or makes folders on the device (a client moves or
copies with LinkFile and DelFile; a folder is only its files' paths) - and `checkNewPath` is the one
rule for all of them, BeginUpload asking it before any content comes: a new file's path starts with
"/", doesn't end in "/", none of its names is "." or "..", and it has no NUL (`BadPathError`,
`dev.files.bad_path`). No client builds a "." or ".." name (otc-sync and the Mac send a cleaned local
path, the phones and the web a folder and a name). An empty name inside a path ("//") is taken, as
before: otc-sync on Windows sends one for a folder on a network share (`\\NAS\Photos` cleaned is
`//NAS/Photos`, its files `/windows/<host>//NAS/Photos/...`), and a web folder typed with "//" has one;
it leads nowhere outside a folder. A path already in `files`, byte for byte, stays its client's to
replace, read and delete whatever it looks like: rows stored before are left as they are, and a share
archive's entry names drop any empty, "." or ".." name and turn a `\` into `_` (`zipEntryName`), so no
entry unpacks outside its folder. The path columns are `utf8mb4_bin`, which MariaDB compares PAD
SPACE: "/a.txt" = "/a.txt " holds and the unique key takes them for one path, so an upload of "notes.txt "
next to "notes.txt" with override replaced the other file's content, and a DelFile of the one deleted
the other. Every lookup, change and delete of one path is now exact (`dao.exactPath`: `path = ? and
length(path) = length(?)`, still a unique-key lookup; `GetFileByPath` checks the row's path), and an
upload or link whose path the key finds under another name is refused (`SpacesNameError`,
`dev.files.name_differs_by_spaces`, `dao.SamePathAs`), a chunked one at BeginUpload, before its content
comes: with override (the sync clients) outright, with no code; without it as a duplicate (`errors.Is`
`ErrDuplicatedFile`, error_code `duplicated_file`, from BeginUpload too), so the phones' photo sync,
the post composer and the web's "keep both" store it under another name, as they always did. Two names
that differ only by spaces at their end can't both be on a device (no collation change: a rebuild of
`files`, `file_versions` and the folder tables on every database, with UNIONs failing on a mix if one
stopped halfway). Lists of paths (`path in (...)`: favourites, collections, thumbnails) still match
PAD SPACE: they name rows the client was just given. Tests: `paths_test.go`, dao's `exactpath_test.go`
(`TestPathsAreExactMySQL` against MariaDB), websocket's `duplicated_file_test.go`.
**Disk space** (`freespace.go`): a write that would leave less than a reserve free - 1 GiB, or 2%
of a disk under 50 GB, at least 64 MiB: room for MariaDB on the same disk, the thumbnails and the
alert itself - is refused before it starts: `BeginUpload` (its size plus what the uploads under way
still have to write, those kept for a resume too - which give way, least recently sent to first,
when it doesn't fit with them: `uploads.roomFor`), `UploadFile` (the same), `GetSharedLink`'s archive, `StartSharedGallery` and a post's
video copy, with `RespEnvelope.error_code` "storage_full" (also for a write that hit ENOSPC) and
`dev.files.storage_full`'s "this needs …, and only … is left". The sync clients don't yet pause on
it. A size is the client's to say: BeginUpload refuses one over a pebibyte (`cMaxUploadSize`, "bad
size"), and what a write needs saturates at MaxInt64 rather than wrapping (`sealedSize` past an
exbibyte, `addSizes` for the uploads under way) - a size near MaxInt64 came out negative, nothing to
write, and turned the reserve off for every upload while its own lived (`TestImpossibleSizesDontWrap`).
What is left is statfs' f_bavail in f_frsize units (package `diskspace`: f_bfree counts ext4's
5% for root, which neither otc nor MariaDB can use - a 4 TB mirror showed ~200 GB free while
uploads failed); Status' raid/disk size is used plus available, so the apps' free (size - usage)
is what can be written, and Settings > Users' share is of that size. `storagewatch` (main instance,
`machine_alerts.go`) raises an Alert at 90% full and at 98% (`dev.alert.storage.*`), the latter
pushed too (`push.storage.full_*`, no figures), each once until the disk goes 5 points under it
(`<storage>/.storage-alerted`).
At every start `sweepOrphanedStorage` removes what interrupted work left at the top
level (`.blob-*`/`.upload-*`, `.post-*` and a video cut's `.cut-*`, archives and `shared/<uuid>` galleries with no
`shared_links` row - none if that query fails): in the background once the listeners are up
(`AfterListening`, from bin/otc.go), reading the flat folder 4096 entries at a time (unsorted),
and only what was last modified a minute or more before this process started (`Manager.started`),
so nothing this run writes - a paused chunked upload, one kept for a resume however long it waited
(#205), an archive being built - is ever taken; a restart forgets a kept upload, and the next
start takes its file.
`integrity.go` checks once a day (a minute after start, then daily) for rows whose content is
  missing - the distinct hashes a page of 5000 at a time from `files` and from `file_versions`
  (`dao.HashesPage`), a `hasBlob` stat each, and the paths of the missing ones only
  (`ContentPathsOf`); every (hash, path) pair used to be loaded first - and raises one Alerts entry
  per change (`.integrity-reported` in the storage path holds the last reported set). The missing blobs found on Cala came with its RAID recovery on
  2026-09-23: they were lost on the previous installation, most likely by the re-upload bug fixed
  on 2026-09-03 (34a8c7d), which deleted the old blob while the row kept its hash. Issue #132's upload-only folders live
  here: `upload_only_folders` (paths with their trailing slash, checked by prefix) refuse
  `DelPath` for anything under them with `ErrUploadOnly` (`RespEnvelope.error_code =
  "upload_only"`, which the sync clients take as "kept on the device": not retried, and a two-way
  folder records it so the file isn't downloaded back - see the desktop section), and a second
  `UploadFile`/`LinkFile` to an existing path there moves the old row into `file_versions`
  (`dao.ReplaceFileKeepingVersion`) instead of failing or overwriting. Blobs are shared by hash
  between `files` and `file_versions`, so `HashReferenced` is the check before one is removed;
  a path's versions go with it when it is finally deleted. Outside those folders an override
  (`registerUpload`'s forceOverride, `LinkFile`) replaces the row in place (`dao.OverrideFile`) and
  keeps any versions; only `DelFile` deletes them. `ListFiles` annotates every entry
  with `upload_only` and `versions`, `ListFileVersions` lists them, and `GetFile.hash` serves
  one. The web, iOS and Android explorers show the lock on folders (a toggle, `SetUploadOnly`)
  and the versions badge that opens the pop-up. Unlocking a folder that a flagged folder above it
  still covers is refused and changes nothing (issue #186: it used to answer ok with the lock
  still on): `LockedByParentError`, error code `locked_by_parent`, a message naming the nearest
  flagged folder above ("unlock /Photos to unlock it", or "first, then Trip" when the folder has
  its own flag too); every explorer shows an error reply's message.
  Issue #192's folders kept out of Images (`out_of_images_folders`, the same path rules;
  `files_manager/out_of_images.go`): content is kept out when a row of it (`files` or
  `file_versions`) is under a flagged folder and none is outside them all. It still gets its
  thumbnail (Files shows it; a processing failure making it is logged, not alerted - a disk
  read/write failure still is, #64) but never tags (place tags included) or faces: `guardAnalysis` takes
  the analysis out at processMedia's two entry points, before anything is read, and records the
  hash in `skipped_analysis`. Flagging (`SetOutOfImages`, synchronous) deletes the tags and faces of
  what only that folder holds (`DelFacesByHashes`: unnamed people left with no face go, named ones
  stay) and takes its rows out of every search token (`keepOut`; a page in flight is filtered when
  it stores; a token leaves them out of the chunks it fetches later too); showing it again moves the skipped content to `pending_analysis` in one transaction
  (`TakeSkippedAnalysis`), then queues it (faces only if face recognition is on now - an explicit owner
  action, not #178's retroactive sweep), and is refused under a flagged folder
  (`OutOfImagesByParentError`, error code `out_of_images_by_parent`). `reconcileAnalysis` applies
  the rule again after anything that changes which paths hold a hash (an upload or link of known
  content, DelFile/DelPath, overrides, an analysis that ran while a flag was set);
  `ReconcileOutOfImages` repairs an interrupted one at the first sign-in; DelPath of a folder clears
  its flags (not a file's of the same name), DelFile never does (a sync app empties a folder file by file). Images - `SearchMedia`,
  the date buckets, collections' count and cover (membership is kept), a collection's gallery; a
  folder's gallery leaves out flagged folders below it, not the shared folder itself - takes the
  folders as `excluded`, from memory (`OutOfImagesFolders`: only this process writes the table), and
  fails rather than show kept-out photos when they can't be read; tags and People need no change,
  the data is gone. `ListFiles`/`SearchFiles` mark `File.out_of_images` and set
  `ListOfFiles.out_of_images_supported` (+ `folder_out_of_images` for a listing). Renaming a flagged
  folder from a computer makes a new, unflagged path (known limitation). Known gap: content whose
  only `files` row is kept out but which a kept version outside holds keeps its tags and faces
  (#132), which the tag list and People show while Images never lists it. Release 108's script
  creates both tables; a database without them reads as nothing kept out.
  **Storage format and chunked transfers** (security advisory on memory exhaustion, releases 40-42;
  a key per file, issue #203, release 164): every blob and thumbnail - and a share link's archive
  and gallery files, under the link's key - is encrypted in 1 MiB segments (`segcrypt`), each
  segment AES-GCM with nonce prefix|index|last-flag and the header as AAD (no reordering, swapping
  or truncation; an empty file's one segment is opened as it is opened), read and written through
  `blobstore` (`Open` gives a `ReaderAt` that decrypts only the segments a read covers;
  `Create`/`CommitAs` seal as data arrives). The first 4 bytes name the format. **OTS2**, all that
  is written since release 164 (`segcrypt.Current`): `OTS2` + a 32-byte random salt (36-byte
  header); the file's own key is HKDF-SHA256(key, salt, info `otc segcrypt OTS2 file key`), 32
  bytes, AES-256-GCM, nonce prefix 7 zero bytes. **OTS1**, releases 40-163, read and never written:
  `OTS1` + a 7-byte random nonce prefix (11-byte header) under the key itself - two files that drew
  the same prefix shared every nonce. No migration: a library holds both, each file read by its
  own header. The key is `segcrypt.Keys` - `AEAD()` (OTS1) and `FileAEAD(salt)` (OTS2): a session
  (the data key, SHA-256 of the vault's secret, held as a `segcrypt.Key`) or a link's `linkKey`
  (SHA-256 of the secret in the link). A size worked out from a header goes by the file's own
  format - `blobstore.FormatOf(path)`, then `Format.EncryptedSize`/`PlainSize` (HasHashes'
  `wholeBlob`, the size backfill) - and one predicted before a write by `segcrypt.Current`'s
  (`sealedSize`, the free-space checks). A binary before release 164 can't read OTS2
  (`segcrypt: not a segmented encrypted file`): rolled back to one (update.sh puts the previous
  binary back when a new one doesn't start), what the newer binary wrote stays unreadable - never
  deleted: no path removes content for not opening - until the device updates again, and its
  HasHashes doesn't count those blobs, so the phones keep their copies. Devices from before
  release 40 were reinstalled rather than converted (release 42 removed the whole-seal reader).
  Files move in
  chunks of at most 4 MiB: `ReadFile` (original bytes by range - `GetFile` remains only to show
  a photo, converting HEIC), `BeginUpload`/`UploadChunk`/`FinishUpload` (encrypted as it arrives,
  SHA-256 checked at the end, then `registerUpload` - the same bookkeeping as `UploadFile`),
  `DownloadSharedLink` with offset/length. **Resumable uploads** (issue #205, release 166;
  `files_manager/chunked.go`, the proto's BeginUpload comment): `BeginUpload.sha256` with the path
  and size is the resume key (`uploadKey`; lowercase hex of 64, anything else is no key). An upload
  with a key outlives its connection: `ReleaseUploadsOf` (deferred in `serveConnection`, after its
  requests returned) keeps it (`kept`, its temp file, open segment, hash and head all in memory),
  and a BeginUpload of the same key on any connection of the instance (each user's is its own
  process, one data key) goes on with the same Writer and upload id from `UploadStarted.received`,
  the latest BeginUpload's dates, override and cloud id winning; one without a key is dropped when
  its connection closes, as before. Ownership is the connection (`owner`, checked with the session
  by UploadChunk and FinishUpload): the latest BeginUpload of a key takes it over even from a
  connection still open (a phone's half-open socket - there is no read deadline on a signed-in
  direct connection - looks alive for minutes), whose further requests for it get "unknown or expired
  upload". FinishUpload checks the hash against what it is sent and against the key. A write that
  failed drops the upload (sealed file and hash out of step). BeginUploads are serialised
  (`uploads.begin`); lock order `uploads.mu` then an upload's `mu`. Limits per instance:
  `cMaxUploads` 32 unfinished in all (~34 MiB of segment buffers and heads; past it the least
  recently sent-to kept one goes, else `ErrTooManyUploads`, `dev.files.upload_too_many`),
  `cMaxKeptUploads` 16 waiting (the oldest go as connections close), kept ones give way for disk
  room (above), and `cUploadIdle` (30 min since the last chunk) drops any. Nothing survives a
  restart (out of scope: the hash state and the open segment are memory). Tests: `resume_test.go`,
  `chunked_test.go`, websocket's `TestUploadResumesOnAnotherConnection` (real sockets, a child
  process). Stored videos reach ffmpeg/ffprobe over the device's
  own loopback stream (`SetVideoSource`: a short-lived media token), so processing, reprocess
  and the info panel never load a video whole nor write it out in plaintext; share-link zips
  are streamed into a segmented file under the link's key. Every zip entry is Deflate; types that
  are already compressed (`zipLevelFor`: video, most audio, JPEG/HEIC/PNG/WebP..., archives) go in at
  `flate.NoCompression`, never `zip.Store` - Go always writes data descriptors, and streaming
  unzippers (Java's ZipInputStream, funzip) reject stored entries that have one.
  `OpenSharedLinkRange` (pre-auth) serves only `kind='archive'` rows with canonical lowercase uuids;
  a link that isn't there (an id unknown or swept, deleted, its file gone, a secret cut short) answers
  a gallery's one sentence, `dev.files.gallery_not_found`, never the raw cause ("sql: no rows in
  result set"), which goes to the log at Debug (`sharedLinkFailure`; a failing disk is "internal
  error", at Error).
  **Thumbnails** (release 111, `files_manager/thumbnail_size.go`): every photo and video has two,
  sealed next to its blob and made from the picture as shown (EXIF orientation applied), never
  scaled up. `<hash>_thumbnail`, the big one: its LONGEST side at most `[otc]
  max-thumbnail-width-px` (`ThumbnailMaxSide()`, default 1000; the key keeps its old name but
  bounds the height too since 111 - a portrait was 1000x1333, a long screenshot 1000 px wide and
  as tall as it was), JPEG q80: Social, shared galleries, video posters, a viewer's thumbnail that
  stays on screen (below) and every answer that doesn't ask for small ones. `<hash>_thumbnail_small`, the grids' tile: shorter
  side 400 px, longer at most 800 (a panorama stays small), q75, the picture's own aspect (clients
  crop it to a square; the device never crops). Measured on 32 photos: big ~104 KB (was ~130 KB
  width-capped, max 415 KB), small ~38 KB (max 70 KB) - a first page of 12 is ~0.46 MB instead of
  ~1.5 MB. Processing writes both from one decode (`writeThumbnails`; the small one from the big
  one's pixels), small first and big last, both under the hash's lock: the big one is what says "processed"
  (`hasThumbnail`, ImageSearch's #147 rule), so `readGridThumbnail` shows a small one only when
  the big one is there. Both go together under the hash's lock (`removeThumbnails`, from
  `removeBlobIfUnused` and `dropIfOrphaned`); kept-out content (#192) gets both. Clients ask for
  the small ones with `small_thumbnails` on `SearchPhotos` (9), `GetThumbnails` (2),
  `ListImageGroups` (1) and `CreateImageGroup` (3); devices before 111 ignore it and send big ones,
  and clients that don't set it keep getting big ones. Every grid asks for small ones (web: Images,
  Files, Collections, the composer, the profile photo picker). A viewer shows the grid's small tile
  only until the full size arrives; where a thumbnail stays on screen (the full fetch failed, a
  photo the browser can't decode, a video it can't play) it asks `GetThumbnails` without the flag
  for the big one, retried like a grid page while it stays on the item (web `MediaViewer`, with
  `usePageRetry`; the apps' viewers follow the same rule). Content without a small one yet is answered
  with its big one AS STORED - made before 111, that one is capped by width only (1000x1333, or 1000
  px wide and any height) until the queue or the pass rescales it, so clients must assume no bound
  on it - and queued (`thumbnail_fix.go`: one worker, at most 1024 hashes, deduplicated) to get one
  from its big thumbnail - making it inline would cost ~60 ms a photo on a Pi 5 before the first
  tile (14 ms on an M-series Mac), as much as the bytes it saves on most links. The queue's worker
  gives way like the pass (below): it waits for quiet before each file, rests after each fix, and
  is emptied by a full reprocess. No size tells the two apart (an 800x600 photo's big one is
  800x600, a panorama's 1000x250), so since release 113 every entry of SearchPhotos and
  GetThumbnails answers says which came: `File.thumbnail_small` (field 13, `optional bool`: 113+
  always sets it, true or false; older devices leave it unset, and clients keep those by what they
  asked for). On an entry without content (`omit_thumbnails`) it only says a small one is stored
  (a stat, not a read): a phone that cached a big one under its small key asks `GetThumbnails` for
  it once when it turns true and keeps what that answer's entry says - the marker on an entry with
  content is the one that counts (a small one stored but unreadable comes as the big one, and is
  not asked for again). The
  one-time pass (`thumbnail_pass.go`, `FitStoredThumbnails`, after `BackfillMissingThumbnails` at
  the first sign-in) does the rest: for every media hash it reads both headers and, only where
  needed, makes the small one and scales a big one over the cap down - always from the big
  thumbnail, never the original (`fixThumbnails`, which decides and writes under the hash's lock,
  skipping content deleted or processed again meanwhile - and, before decoding, content whose blob
  is gone (#141), which would otherwise be decoded for nothing at every grid's request - and holds the content budget for what it
  allocates, `thumbFixReserve`: the decode (3 B/px), x/image/draw's kernel scaler buffer (32 B x
  dstW x srcH per scale - most of it: an old 1000x1333 portrait allocates ~51 MB) and the copies,
  checked against the real allocations by a test; a 1000x30000 thumbnail holds ~124 MB and waits
  for downloads instead of adding to them; over 500 MP, `cMaxImagePixels`, it is skipped). It waits while uploads arrive (`noteUploadActivity`, a
  minute) or the lanes are busy, rests as long as each fix took (at least 50 ms), stops for a full
  reprocess, and records `<cap> <small side> <last hash>|done` in `<storage>/.thumbnails-sized` -
  resumable, run again when the small size changes or the cap goes down. Posts' copies in
  `unenc-storage-path` and shared galleries keep what they were made with.
  **Files grid** (release 80): the Files section on the web, iOS and Android switches between the
  list and a grid (remembered per browser/app). The grid shows each photo or video by its
  thumbnail and everything else by a generic labelled document icon whose colour says the type
  (PDF red, DOC blue, XLS green, PPT orange...; the mapping is the same in `FileTypeIcon.tsx`,
  `FileTypeIcon.swift` and `FileTypeIcon.kt` - change all three together; no vendor logos).
  `GetThumbnails{paths}` (their rows in one `in` query, `dao.GetFilesByPaths`; a path at a time
  should it fail) answers up to 48 paths (about 8 MB) per request with the stored
  thumbnails, in the order asked, leaving out paths without one; since 113
  `ListOfFiles.ask_again_from` (5) is the index of the first path it didn't look at (the byte cap,
  or past 48), 0 when it looked at all - before, a cut answer looked like paths without
  thumbnails. It stays by path, for the phones' cache misses too: every grid has each photo's
  path, each entry carries its path's current hash to key a cache by, and a lookup by hash would
  still need a query to know the hash is in use.
  **Storage view** (issue #195): `GetFolderUsage{path}` (139) answers `FolderUsage` (69), asked
  only while Files' Storage view is open - ListFiles never sizes folders. Same callers as
  ListFiles (the owner, a user on their own instance, never a friend); older devices answer
  `unknown_payload`. `files_manager.FolderUsage` takes the folder as ListFiles does (the trailing
  slash added, `""` is the root) and answers every direct child holding a file or an older
  version: its path as ListFiles lists it, `is_dir` (a file and a folder of one name are two
  entries), `files_size`/`file_count` of every file under it at any depth, and `versions_size`
  (`file_versions`, not part of `files_size`); largest first (files + versions), then by path
  byte by byte, a folder before a file of the same name; plus the folder's sums. Sizes are
  logical (`size64`): content at two paths counts at both; thumbnails, encryption, posts,
  galleries, other users and the database aren't counted. `dao.FolderUsage` is one statement
  that reads each table once by `underPrefix`'s range; the database only cuts each path down to
  its child and Go sums (a GROUP BY was 4-8x slower: MariaDB copies the 768-character key into
  its temporary table for every row). `BenchmarkFolderUsageMySQL` (100k files and 10k versions,
  Lima VM): 22 ms at the root, where listing the root takes 114 ms, and 22 ms for a 45k-file
  folder. The answer has no upload-only or kept-out flags: the apps join it with their ListFiles
  listing by (path, is_dir).
  **Largest files** (release 177, Settings › Largest files; `files_manager/largest.go`,
  `dao/largest.go`): `ListLargestFiles{after, limit}` (166) answers `ListOfFiles` - every content
  in the library once however many paths hold it, the largest first, at the same size by hash,
  as its row at its first path in byte order (`dao.FilesOfHashes`), with `File.copies` and
  `original_hash`/`original_removed` (`setOriginals`, for the viewer's Revert), without content
  (thumbnails by path, as Files' grid). Every file counts - documents, folders kept out of Images -
  and older versions don't. Keyset pages (`limit` 1-200, 0 is 50): the token is `<size>:<hash>`
  of the last content the page read, "" once the list is over; a token that doesn't parse is the
  first page, never an error, and the hash is taken as it is (one that isn't a SHA-256 doesn't
  stop the pages). One `select hash, max(size) s from files group by hash having s < ? or (s = ?
  and hash > ?) order by s desc, hash limit ?` a page: there is no index on size (release 143),
  so each page groups the hash index and sorts. Measured in the Lima VM (MariaDB 11.8, 170k rows,
  150k contents, `TestLargestFilesLargeMySQL` with `OTC_TEST_BROWSE_LARGE=1`): 110-120 ms a page
  of 50 (the first 290 ms) - a Pi is 3-4x slower; an index on (hash, size) made it 67 ms, one on
  (size, hash) read as a keyset over rows 47 ms. The demo allows it.
  **Logs** (Settings > Logs, web/iOS/Android, main instance only): `GetLogs{source "app"|"update",
  offset, max_bytes, wait_seconds}` reads the device's log (`[logger] log_file`) or the update log
  (`/var/log/otc-update/update.log`) in whole lines; with `wait_seconds` and nothing new the device
  holds the request until the log grows (25 s at most), so the clients' loop of requests is a live
  stream that the bridge relays unchanged. `SendLogs{note}` gzips the end of both logs and sends
  them with `BridgeSendLogs` (device secret) to the bridge, which mails them to info@off-the.cloud
  with the owner's account email as Reply-To (3 an hour per device). The screens warn that logs can
  include file names, search words, Wi-Fi names and addresses.
  **Processing lanes** (release 73, `files_manager/lanes.go`): an upload is answered once its
  bytes and row are stored, then `enqueueMedia` (media only) records its hash in
  `pending_analysis` and queues it in the *fast lane* (NumCPU-1 workers, at most one per 2 GB - `processingWorkersFor`; the
  slow lane at most 3; ffmpeg decodes frames with a Pi's 4 threads: a 32-thread PC ran 31 a lane,
  more than 32 GB hold, until release 133; one job at a time across
  both lanes in low-memory mode and for an hour after a death - see Low-memory profile; and, on
  every device, no job beside others without the memory for it - see Memory admission): EXIF, decode,
  orientation, thumbnail - one frame for a video. It then moves to the *slow lane* (tags,
  faces; four frames for a video), whose workers only take a job while the fast lane has none
  queued or running, so during a big sync every thumbnail comes first. `processMedia(...,
  stages)` is the one pipeline (`processMediaContent` = both stages from one decode, for
  backfill and Reprocess, and clears the pending row). The queue itself is memory, but
  `pending_analysis` holds only hashes, so after a restart - when nothing can be decrypted
  until the owner's key is back - `ResumePendingAnalysis` refills the lanes at the first
  sign-in (`startBackfillOnce`, before `BackfillMissingThumbnails`, which skips those hashes),
  reading `pending_analysis` 500 at a time in the order it was queued (`dao.PendingPage`) and each
  page's files in one query (`dao.FilesOfHashes`, the smallest path of each; a page that can't be
  read stays pending for the next start). Issue #206: a job is only its hash (`mediaJob{ses,
  hash}`, ~90 bytes - a big sync queues tens of thousands), its row read when it runs (`jobFile`:
  the smallest path that is media; gone, or none media, its analysis is no longer pending; a row
  that can't be read leaves it pending), so a file deleted while its job waited is never read.
  `UploadFile`/`FinishUpload` don't queue content the device already knows and has a thumbnail for,
  and `processFaces` skips content that already has faces.
  Issue #180 (release 69): **shared galleries** - an image group (`group_id`), a folder
  (`directory`, recursive) or files (`paths`) are copied by a background job
  (`files_manager/shared_gallery.go`: `CreateSharedGallery` then `GetSharedGalleryJob` polling)
  into `<storage>/shared/<uuid>/` - `manifest`, `<i>.orig`, `<i>.thumb`, and `<i>.prev` (a JPEG
  for what browsers can't show: HEIC, RAW, TIFF) - all sealed under `getCipher(secret)`. The
  link is `https://<domain>/shared#<uuid>.<secret>`: the secret only ever lives in the link's
  fragment (never sent to a server, never stored); `shared_links` keeps the uuid, `kind`
  (archive/gallery), `description` (under the owner's key), `files`, `opens`, `last_opened`,
  `expires` (1/7/30 days, default the old TTL). The web route `/shared` renders
  `SharedGalleryView` before anything else of the app, for anyone: thumbnails, a viewer (photos
  by their preview, videos streamed through a `/media/<token>` minted for the gallery file
  under the link's key), download all or a selection. Visitors' requests
  (`OpenSharedGallery`, `GetSharedGalleryItem`, `GetSharedGalleryStream`) are public and answer
  every failure identically. Owners: share from an image group or a folder (web, iOS, Android:
  `SharedGalleryShareFlow`), and Settings > Shared Links (`SharedLinksView` /
  `SharedLinksPanel`) lists every link with its opens and size, and deletes a link with its copy.
  Decrypted manifests are cached (8, 5 min idle, valid only for the same secret and manifest
  size/mtime), but the expiry is read from the database on every request.
  An archive's link is `/#download=<uuid>_<secret>` (2026-10: the secret in the fragment, as a
  gallery's; `downloadLink.ts` still takes `?download=`). Its page is `ZipLinkPage.tsx` (rules in
  `zipDownload.ts`, `node web/scripts/zip-download-test.mjs`), in the tab Images' and Files'
  Download ZIP open at the click (`zipTab.ts`); a part with no answer is asked again after 1, 3 and
  6 s, and past that the page says so with Try again, which goes on from that part (a reload
  started over and counted another opening of the link): the percent rounded down (100% only
  with every byte
  in), then the file handed to the browser and a tick, "Your download has started" and a Download
  button (an `<a download>` on the same `blob:` URL, kept while the page shows) that saves it again;
  2 s later the tab closes itself only when nothing took the focus or changed its visibility
  meanwhile and it is in the background or focused (`watchHandOff`, `mayClose`) - a browser asking
  where to save, or whether the site may download, keeps the tab and its question. Releases up to
  140 stayed on "Downloading… 100%" (user feedback 2026-10: "stalls at 100%, the file did
  download"); 141 closed the tab regardless, with any such question.
  Issue #166 (release 63): a share link is downloaded in parts (the web page asks for 4 MiB
  ranges; a whole-archive `DownloadSharedLink` is refused above 4 MiB), and every reply carrying
  file content holds the content budget until it is on the wire (`connHandler.reserveMemory`:
  `GetFile`, `GetPublicationMedia`, share-link parts; `GetFileInfo` reserves while it reads a
  photo). One reply never holds a file too large for that (`membudget.go`; acquire lets a request
  over the whole budget run alone, and a 1.5 GB TIFF in a viewer, or a friend syncing a multi-GB
  post video, killed the device): `GetPublicationMedia` refuses a post's file over
  `MaxPublicationMedia` (256 MB, or a third of the budget: 85 MB on a 4 GB Pi) and `GetFile` one over
  `shownWholeLimit` (a third of the budget, at least 64 MB), whatever it is - a video and a HEIC (read
  whole to be converted) too since 2026-11: the web composer's trimmer streams a library video or
  reads it in ReadFile pieces since release 143, as the viewers do, and the apps ask GetFile only for
  photos (iOS's `FileDownload` and Android's `ChunkedDownload` fall back to it only on a device from
  before ReadFile), the sync apps never - with
  `RespEnvelope.error_code` "too_large" (`websocket/content_codes.go`), reserving nothing. The
  viewers keep the thumbnail, downloads read in parts, a friend's device keeps the post's thumbnail
  (its sync logs the refusal; fetching it through GetMediaURL is still to do). `GetFileInfo` reads a
  photo's EXIF through the blob's ReaderAt (`exifFrom`, by its first bytes: a JPEG up to its EXIF
  segment, a HEIC its boxes, a TIFF at its directories wherever they are - exifinfo's `compactTIFF`;
  the byte-by-byte search for an EXIF segment goes no further than `shownWholeLimit`), and
  `exifinfo.FromVideoSource`'s ffprobe has a deadline (`ProbeTimeout`, a minute). Processing reads an
  image whole only within the content budget (2026-11; it read any size, held from nothing, and a 2-3
  GB scan or Photoshop file took a 4 GB device down at every start until it was set aside): the lanes,
  Reprocess and the thumbnail backfill hold its size from the content budget until it is decoded
  (`readForProcessing`; `processMedia` gives it back right after the decode, so the thumbnails, a wait
  for the tagger, the tagging, faces and the rest don't keep downloads waiting) and read nothing over
  the decode budget's size (`processedWholeLimit` - such a picture never fits its decode),
  which is alerted as not processed, "the file is 2.1 GB, more than this device can process at once
  (at most 760 MB)" (`dev.files.too_large_to_process`); a shared gallery's preview, read whole as
  GetFile reads a photo, stops at `shownWholeLimit` (the item keeps its original and thumbnail).
  ffmpeg's fallback for stills still gets its own temporary copy. A post's video is
  never loaded: `ExportVideoForPost` re-encodes it from the loopback
  stream straight into the posts' directory (named by hash) or decrypts the original there a
  segment at a time, one transcode at a time (`transcodeSlots`, taken before the stream token).
  The re-encode gets six seconds a second of output, half an hour at least (`transcodeTimeoutFor`,
  the length from ffprobe), and once more after a timeout with `transcodePlan.fast` (ultrafast,
  `-skip_loop_filter all`, no HDR tone mapping); if that fails too, the original is published in its
  place only when no cut was asked for and it is within `MaxPublicationMedia` - otherwise the post
  fails, saying why (`dev.files.video_not_cut`, `video_not_reduced`): a cut is never undone. Both
  post copies are synced before their rename, the folder after it. A post's photo that needs no
  converting is copied a segment at a time (`CopyForPost`), a HEIC read whole within the budget.
  `social.NewPublication` holds the hashes it writes (`social/post_files.go`, `postsInFlight`) and
  takes its plaintext copies back when the post isn't made (`postFiles.discard`: not a file another
  post names or another post being made holds); once per start, before the first friend sync
  (websocket's `backgroundWorker`), `SweepUnusedPostFiles` removes `<hash>` and `<hash>_thumbnail`
  files older than a day that no `social_publications_files` row names (the only table naming them;
  the alerts' thumbnails are their posts') - nothing else in the folder. The same post sent again
  is made once (`social/post_once.go`; NewSocialPublication carries no id of the client's): a long
  video's cut and shrink can outlast the answer - a dropped socket, the bridge's 90 s - and publishing
  again made it twice for every friend. Its text, its files' content hashes (the `files` row at each
  path; the path when there is none) and their cuts are the key (`postKey`; the device has one owner,
  whichever app or socket sends it) - content, not paths, since the web app uploads a file from the
  computer to a new `/web/` path each time and a composer closed by `device_unreachable` sends it
  again: sent while the first is being made it waits for that one's answer, within
  `cPostAgainWindow` (10 minutes) of it being made it gets its uuid at once; one that failed is made
  again, and deleting the post forgets it. `budgetSize` uses the row's int64 size and
  looks at the blob only until the size backfill below has run. Since release 93 (`93.sh`, and
  `install.sh` `apply_schema_migrations`) the size columns of `files`, `file_versions` and
  `social_publications_files` are BIGINT; `files_manager/size_backfill.go` corrects, once and in
  the background at start until `settings.sizes_backfilled` is set, the rows stored wrapped (or,
  for friends' posts in release 92, clamped) before it: only blobs over MaxInt32, content size from
  the size on disk by the blob's own format (`blobstore.FormatOf`, then `PlainSize`; no key
  needed), own and friends' post files from their
  copies in `unenc-storage-path`; it waits for the BIGINT columns, and a pass with an error is
  retried at the next start. `checkImageSize` also bounds a HEIC
  grid by its decoded first tile (`checkHeifGrid`). **Large photos** (2026-10-24, `limits.go`,
  `decode_cost.go`; until then a fixed 120 MP limit refused a phone's 200 MP photos): what an image
  may be goes by the device's memory. Every still decode (`decodeImage`, `heicToJpeg`) holds what it
  costs from its own budget, a fifth of MemTotal (1.6 GB on 8 GB, ~760 MB on 4 GB, 1 GB unknown;
  `holdDecode`, waiting for room - separate from the content budget, which downloads and gallery
  previews hold first), and an image whose decode alone is over the whole budget is refused, the
  message saying the device's limit for that kind ("over N megapixels"); 500 MP is refused
  everywhere (`cMaxImagePixels`). The cost is read from the header (`decodeBytesPerPixel`, matched
  to Go's real allocations in `TestDecodeReservationCoversWhatItAllocates`): a byte a sample for a
  JPEG (`jpegFrame`: 1.5 a pixel at 4:2:0) plus 4 a sample if progressive, a PNG's pixel size (x2
  interlaced), 8 for a HEIC. A photo over 50 MP (`cWorkPixels`) is box-shrunk by a whole factor
  as it is decoded (`shrinkForWork`, +1 byte a pixel) and the full decode freed: thumbnails, tags,
  faces and gallery previews work on at most 50 MP, so what follows costs what a 50 MP photo
  always did. A 200 MP phone JPEG: ~490 MB held, 1.9 s on an M-series Mac; limits per kind in
  `decodeBudgetBytes`' table (4 GB: 311 MP of 4:2:0 JPEG, 91 MP progressive, 86 MP HEIC). The
  backfill's `cDecoders` gained `by-memory`, so photos refused before are tried once more. Decode stills through `decodeStill`
  (upload processing and the shared-gallery preview): ffmpeg only for what Go can't read, never
  for anything `checkImageSize` refuses (issue #188) - ffmpeg would decode it anyway, unbounded -
  nor for content no ffmpeg reads (`neverPreviewedMimes`: DjVu, CAD drawings, GIMP files), which
  `isMedia` also leaves out, so it is never read, queued (`enqueueMedia`, `enqueueAnalysis` and
  `ResumePendingAnalysis` drop it; dao's `cMediaRows` doesn't exclude it, so #192 may record it as
  skipped) or given a thumbnail; other image/* types
  (`.ico`, `.svg`, AVIF...) are left to whatever the device's ffmpeg build decodes.
- `raidwatch` — a disk of the mirror (md0) that stops working: an Alert of its own
  (`dao.AddStorageNotification`, an ungrouped Error row) and a push naming the USB port it is in ("the
  disk in the top blue USB port"), once per failure (`<storage>/.raid-alerted`), and another once the
  mirror is whole again; the status line names it too (`raidwatch.Missing`). The alert and push go
  out through `Notify.AlertText` (an `alerttext.Alert`) and `Notify.PushText` (a `push.Text`);
  `Notify.Alert`/`Push` get the English when those are nil. Read from sysfs, no root:
  where each disk is plugged in is recorded once every disk is in the mirror, during a new mirror's
  first copy too (md counts both in sync then; `<storage>/.raid-members.json`), since a failed reader
  usually vanishes. With nothing recorded, the disk still working is named instead when it is in a
  blue port (`raidwatch.StillWorking`; `dev.alert.raid.stopped_detail_working_*`,
  `dev.status.raid.degraded_working_*`, `push.raid.stopped_working_*`). Main instance only.
  The storage log (release 138): `raid_watch.py` writes what it prints, with times, to
  `/var/log/otc-storage/storage.log` (root-owned, 0644, 512 KB and a `.1`) - the disks the Pi sees
  with their USB port and speed, the mirror's state in one line, why a disk wasn't added, a
  rebuild's progress every 10 minutes, and the kernel's lines about USB, the disks and md
  (`journalctl -k`, every 30 s and on any change) - because the service can't read the journal and
  root must not write in its log directory; `raidwatch.FollowLog` copies each new line into the
  device's own log as `storage: …` (Settings > Logs > Device, every app; its place kept in
  `<storage>/.storage-log-read`, by the file's first line). `python3 scripts/raid_watch.py
  --selftest` tests the script's side. Its loop never waits long on a disk: every command that reads
  one goes through `run(..., timeout=)` (`run_bounded`: `mdadm --detail`/`--examine` 10 s, `blkid -p`,
  `wipefs`, `dumpe2fs` and `journalctl` 20 s, `mdadm --add`/`--grow` and Settings' `wipefs -a` 60 s),
  a disk's two ends are read in a child (`ends_blank`: the script again with `--ends-blank`, given
  up after 20 s), a `mdadm --detail` given up leaves the last answer for 30 s, and a rebuild's source
  is md's own in-sync member (`rebuild_source_slot`; each member's superblock used to be read every
  half second while rebuilding). The poll runs under `keep_running`: anything it raises is logged
  with its traceback (once per kind, then counted) and the poll starts again after 1 s doubling to
  30 s - one unreadable superblock in a rebuild used to end the process - and `raid-watch.service`
  restarts it 5 s after any failure with no start limit (`RestartSec=5`, `StartLimitIntervalSec=0`:
  install.sh, Makefile.pi, and release 169's script for the units installed before;
  `updater/raid_watch_unit_test.go`). The Pi 5 has one USB controller per blue
  port (`bluePort`: xhci-hcd.0/1 -> top/bottom); which is on top is still to be confirmed on hardware -
  the startup log names each disk's port. The root `raid_watch.py` still drives the LEDs and re-adds a
  replacement - since release 143 only a blank one (no partition table, no `blkid -p`/`wipefs`
  signature, first and last MiB all 0x00 or 0xFF, read with O_EXCL|O_DIRECT) or a former member of
  md0 whose superblock doesn't show it ran without the disk md0 runs on now (`judge_disk`, the same
  rule as `otc-mirror`; a verdict is kept while the disk stays plugged in, disks are looked at every
  5 s, and only once md0 has run for 10 s - `running_settled`: never while udev still gathers disks
  plugged back in). Any other disk (a backup drive, a card fresh from the shop, another array's disk, a split
  copy) is left alone, logged once, and listed in root's `/run/otc-raid-watch/not-added.json`, from
  which `raidwatch` raises one Alert per plug-in (`dev.alert.raid.not_added_*`,
  `<storage>/.raid-not-added`); the stopped alerts' details say a replacement that isn't blank is
  erased once the owner confirms in Settings (`dev.alert.raid.replace_detail*`; the old
  `stopped_detail*` keys stay for old rows). Settings › Storage (web `settings/MirrorDiskPanel.tsx`,
  iOS `StorageSection.swift`, Android `ui/settings/StorageSection.kt` - issue #208; two disks of one
  name and size get no button): only while md0 has a
  slot with no working disk - not while a replacement is copied onto, when md still says degraded
  (`mirror_needs_disk`/`needsDisk`, which also gate the automatic add now) - `Status.mirror_candidates` (19: an opaque id, `sha256(boot id|name|diskseq|
  size)`, the model and the size; primary owner only) lists the disks not-added.json offers (`offer`;
  none `held`: a mount, swap or holders, whose Alert says `not_added_detail_in_use`). `UseMirrorDisk`
  (145, raidwatch `UseDisk`: `not_degraded`, `not_candidate`, `busy` while one waits) links
  `{"v":1,"id","disk"}` to `/run/otc/mirror-disk.request` and answers at once; `raid_watch.py`'s
  loop takes it (renamed into its own directory, O_NOFOLLOW, the otc user's, one link), acts only on
  a disk it offers now, checks it again (the same attachment by `diskseq`, also through an
  O_EXCL open's BLKGETDISKSEQ, and size; not held, not the system's or the mirror's, big enough, the
  mirror still lacking a disk), then `wipefs -a` each partition and the disk and `mdadm --add`s it,
  answering in `/run/otc-raid-watch/mirror-disk.json` (`added`, `gone`, `not_degraded`, `in_use`,
  `failed`, `bad_request`): an Alert unless added (`dev.alert.raid.use_*`, once per answer,
  `<storage>/.raid-use-answered`), and a request not taken in a minute is withdrawn with one. First-sign-in
  storage setup (`OTC_SKIP_RAID` hand installs) refuses storage that already holds files, a disk with a
  signature, partitions or an md superblock, and storage fstab already names; it reads its request with
  O_NOFOLLOW|O_NONBLOCK, gives the new filesystem to otc (0751, `unencrypted/` 0750, set on each
  directory opened O_NOFOLLOW) and writes fstab by `UUID=` with nofail and the drop-ins below
  (MariaDB stays on the SD card there). A try cut short after the mount (blkid without a UUID yet, a
  full card) is finished by the next one (`_finish_setup`: the storage given to otc, the mirror's
  ARRAY line in mdadm.conf and the initramfs - `_record_array`, `mdadm --detail --brief` - fstab's
  line and the drop-ins), only for the disk or mirror the request named and only while fstab has no
  line there: it used to take the mount for a finished setup, and after a reboot otc and MariaDB
  started on the empty directory under the mount point. Tested on
  `scsi_debug` disks in the Lima VM (`per_host_store=1`, or every disk shares one store). More storage:
  swap the cards for bigger ones one at a time (each is added and
  rebuilt like a replacement); once every member has room to spare, `raid_watch.py` grows md0
  (`mdadm --grow --size=max`) and the ext4 on it (`resize2fs`, online), checked every 10 minutes, never
  while degraded or rebuilding (tested on loop devices in the Lima VM). Disks go in the two blue USB ports (README, website).
  The storage itself missing (issue #207) is `raid_watch.py`'s `StorageGuard` (the paragraph on
  nofail at the top): it looks every 2 s, writes what it does to the storage log, takes down a
  storage gone while mounted (both disks pulled, the single disk pulled or failing: `storage_gone`,
  same paragraph), and keeps `/run/otc-raid-watch/storage.json` (`{"v":1,"missing_since"}`, root's,
  0644) while the storage has been missing for a minute (at once when it went while mounted), which
  the companion reads for when the outage began.
- `nostorage` — the device without its storage (issue #207). `Ready(storagePath, ping)` is ""
  when the storage can be used: the path is under no mount fstab names (`wantedMount`: the
  deepest, never `/`, no noauto or swap - a container's volume or an `OTC_SKIP_RAID` directory has
  none) or that mount is a whole filesystem on top in `/proc/self/mountinfo` (its root `/`, not
  ro: the companion's read-only bind of the empty directory is not the storage), the path passes
  `access(W_OK|X_OK)` (EROFS once ext4 remounted it read-only) and the database answers
  (`dao.Ping`, 5 s). `bin/otc.go` opens the database without waiting (`dao.Open`; `dao.Init` =
  Open + `WaitReady`, still used by `init-owner-password`/`init-profile`) and calls `Wait` before
  anything touches the storage: it looks every 5 s, and after `Grace` (60 s) serves the apps
  (`serveWithoutStorage`: `websocket.NewUnavailable` + `api.ServeUnavailable`) until ready, then
  stops that and the start goes on in the same process. The outage is recorded on the boot disk,
  `<working dir>/.storage-missing.json` (0600, `{"v":1,"since","last","boot"}`; never on the
  storage): `Begin` keeps an earlier start (a restarted companion, a reboot with the disks still
  out), `Touch` notes it once a minute; the main instance's next full start (`machine_alerts.go`)
  `Report`s it as an Alert (`dev.alert.storage.missing_minutes`/`_hours`/`_days` - minutes up to
  119, hours up to 47, then days - with `missing_detail`; an Error row through
  `AddStorageNotification`, never pushed) ending now in the same boot, else when last seen
  missing, and removes it once stored. A user's instance waits the same, with no record and its
  own identity only. The answer is `websocket.CodeStorageUnavailable` with
  `dev.err.storage_unavailable` in the request's language, the contract with the apps (they show it
  where "device unreachable" goes, ask again every 30 s, never clear anything for it); the
  bridge's GetStaticAsset is the one request served (the web app is on the boot disk).
  `websocket.Unavailable` is one `Manager` per identity with `unavailable` set (processMessage
  answers before anything else), its own pool (`cUnavailablePool`, 8; `bridgeHost`, `poolTarget`)
  and `Stop` (closed: no more dials; idle and taken connections closed). Its identity is the copy
  the full instance keeps beside `.bridge-addresses.json`: `.bridge-identity.json` (0600, `{"v",
  "bridge", "owner_uuid", "domain", "secret"}`), written after a registration the bridge took when
  it differs (`keepIdentity`: a rotated secret, a new name, the bridge switched on), removed when
  the instance runs without a bridge or leaves it (`dropIdentity` in `switchToLocal`; a deleted
  user's home goes whole), and used only for the relay `[otc] bridge-addr` names now; the main
  instance also registers its users' copies (`/var/lib/otc/users/*/`). Without a copy (a device
  never started on this release) it answers on its port only. `api.ServeUnavailable`: the web
  app's files (`StaticHandler`), `/ws`, 503 for `/check_healty` (so update.sh's start check never
  counts it), `/media/` and the metrics; a port still held is tried every 5 s. Tests: nostorage's,
  websocket's `unavailable_test.go` (a fake bridge in a child process), api's
  `TestServeUnavailable`, raid_watch's `selftest_storage`, `selftest_storage_gone` (a fake sysfs:
  members pulled, offline, faulty, a broken array, ext4 errors, btrfs/NFS/FUSE never judged, the
  takedown on a fake clock) and `selftest_assemble` (disks back before the stop handed to udev), updater's
  `TestStorageUnitsAreTheSameInInstallAndRelease166`.
- `wifiwatch` — the Wi-Fi watchdog (release 115). Pit (Pi 5, brcmfmac over SDIO) after ~27 h up
  kept receiving at 6.6 MB/s but sent at 0.15 MB/s, 7.7% of TCP segments retransmitted
  (`CMD53 sg block write failed -84`); a reboot fixed it. Main instance only, only while the IPv4
  default route is a wireless interface. Once a minute: TCP RetransSegs/OutSegs from
  `/proc/net/snmp` with loopback's tx packets taken off OutSegs, judged only with >= 1000 segments
  sent in the minute (idle never counts); bad >= 4%, healthy < 1%, in between holds a streak; quiet
  minutes are tolerated up to 10 (`MaxGap`). Restart after 5 bad minutes in a row when the radio
  confirms (>= 10 tx errors+drops on the interface - brcmfmac counts a failed SDIO write there - or
  >= 5% of frames unacknowledged in `iw dev <if> station dump`), 10 without; never while an update
  runs. All overridable in `[otc]` (`wifi-watchdog = off`, `wifi-watchdog-*`, README). Once per
  episode (the owner's rule): phases armed -> restarting -> restarted -> armed (5 healthy minutes
  after a 2-minute settle) or gave_up (bad again: no second restart until 5 healthy minutes re-arm
  it). Persisted in `<working dir>/.wifi-watchdog.json` with any Alert not yet stored
  (`pending_alert`, an `alerttext.Alert`; an older `{title, detail}` loads as English only) and
  the speed before the restart (`slow_speed` `{rate, pct}` as numbers for the Alert's keys, next to
  the English `slow`). Alerts (`AddErrorNotification`) come after the restart, once judged: "The
  Wi-Fi was restarted: sending data had slowed down: ... It is working normally again." (or, after
  an hour without enough traffic to judge, "...There hasn't been enough traffic since..."), "The
  Wi-Fi is still slow after a restart" (Restart button / Ethernet), "could not be restarted" (root
  failed or silent for 10 min) - never at the request. The root side
  (`scripts/device-runner/otc-wifi-restart-runner.sh`, `otc-wifi-restart.path` on
  `/var/lib/otc/wifi-restart.request`, answer in `wifi-restart-status.json`: running/done/failed/
  rebooted) reads nothing from the request: interface from `/proc/net/route` (refused unless
  wireless), driver module from sysfs (holders first, e.g. brcmfmac_wcc), the SDIO controller
  (`.../mmc_host/...`'s platform device) unbound and bound again only when no block device sits
  under it, the module back, power save off, NetworkManager reconnect; not connected within 150 s
  -> it reboots (`rebooted`, mentioned in the Alert). The whole restart reaches that fallback within
  `RUN_BUDGET` (7 min) of its start, inside the unit's `TimeoutStartSec=8min`: every step goes
  through the runner's own `bounded` (given up, killed and left behind when stuck in the kernel - a
  wedged SDIO bus holding modprobe or the unbind -, never waited for), and none starts past what
  the last wait for the connection needs (`FINAL_WAIT`, 90 s); that wait's own checks
  (NetworkManager's `GENERAL.STATE`) use its time up to the deadline itself (`FINAL`: before, they
  were refused for want of time once the steps had used theirs, and only a default route counted),
  and a connection back at its very end still gets power saving off, at most 10 s past it. The bare
  `timeout`s it had waited for good on such a command, and its waits added up to more than the
  unit allows, so systemd could kill it before the reboot (`TestWifiRestartRunnerEndsInsideItsUnit`;
  `bash otc-wifi-restart-runner.sh --selftest` on Linux tests `bounded`, `left` and the last wait's
  checks). At most one restart per 30 min
  (`/run/otc-wifi-restart.last`). Tested with fake readings (`wifiwatch_test.go`) and
  `mac80211_hwsim` in the Lima VM; the SDIO reset itself only on a Pi. A Settings › Wi-Fi change
  (release 140) never builds a streak: while `network.Joining()` is true - a change in flight and
  the 10 minutes after it ended - `step` resets the streak and returns (the `Watchdog.joining`
  seam, set to `network.Joining` in `Watch`). The restart runner takes `/run/otc-wifi.lock` (the
  Wi-Fi change's lock) and answers `busy` (`dev.status.wifi.busy`) when a change holds it, which
  wifiwatch takes as "try later": re-armed, no Alert, no gave_up.
- `network` — Settings › Wi-Fi (release 140): the owner changes the network the device uses, or its
  password - typically after changing the router's - from the web app (iOS and Android with their
  next store builds). A cable is the fallback once the device has lost its Wi-Fi (the FAQ and the
  apps' "can't reach the device" screens say so: plug it into the router, open Settings › Wi-Fi,
  unplug).
  - Protocol: `GetWifiStatus` (143) and `JoinWifi` (144) both answer `WifiStatus` (71: `managed`,
    the current `ssid` and `signal`, `wired`, and `last_join` = `WifiJoin{id, ssid, state
    joining|connected|failed, code, message, restored}` while a change runs and 15 minutes after).
    `ListWifiNetworks` (53) feeds the picker: one row per name, current first, then by signal,
    `current` and `unsupported` (802.1X, WEP, OWE) set, names that aren't valid UTF-8 dropped (proto3
    would fail the whole list). `JoinWifi.password` is `bytes`, RSA-OAEP sealed under the
    connection's `GetPubKey` key like `CreateUser.password`. One rule everywhere (Go, runner, the
    three apps): SSID 1-32 bytes of UTF-8 with no rune below 0x20 or 0x7f; password empty (open
    network), 8-63 BYTES of UTF-8 without control characters, or exactly 64 hex digits - the apps
    count bytes. `SetWifi` (54, the old first-run step's plaintext password) is retired: answered
    `unknown_payload` without logging the request (the `default:` branch would log the password);
    the web's first-run Wi-Fi step is gone and `RequestJoin`'s plaintext file on the SD card with
    it (140.sh deletes `/var/lib/otc/wifi_join_request.json`).
  - Who: the owner of the primary instance. A child instance gets `managed=false` / `notAvailableHere`,
    a container `managed=false` / `notInContainer`, the demo `managed=true, wired=true` / `demo`.
    `Managed()` (cached 30 s) needs: not a container, `RUNTIME_DIRECTORY=/run/otc` writable,
    NetworkManager running and not netplan-backed (no `/etc/netplan/*.yaml` with `renderer:
    NetworkManager` - one otc can't read counts - and no `90-NM-*.yaml`: its saved profiles land in
    netplan), a wifi device not named `uap*`/`p2p*`, and `otc-wifi-join.path` active; it logs the
    reason when off. Raspberry Pi OS (Trixie) ships NetworkManager 1.52.1 built with netplan, but its
    own patch (`network-manager 1.52.1-1+rpt4`, `0012-fix-netplan.patch`) writes to netplan only
    profiles whose file name starts with `netplan-`, so the runner's profiles stay keyfiles; the
    image purges cloud-init (and `rpi-cloud-init-mods`' netplan file with it), so it has no netplan
    file and is managed - a hand-installed Pi OS set up by cloud-init isn't. The service logs
    `nmcli --version` at Info once at start.
    Devices before 140 answer `unknown_payload`: the apps hide the section until the next sign-in.
  - Go side (`Join`): refusals before anything changes, each with an `error_code` - `bad_name`,
    `bad_password` (after decrypting), `not_managed`, `update_running` (`updater.Pending`: its
    request still waiting or a run going on), `busy` (one change in flight, or a request file still
    there), `too_many` (6 accepted in 15 minutes) - the update check, the in-flight check and the
    handover all under one mutex, which `ApplyUpdate` holds too while it checks `InFlight` and writes
    its request (`network.UnlessChanging`), so neither slips past the other. The request `{"v":1,"id","ssid","psk","hidden"}` goes to a temp file in
    `/run/otc` (tmpfs; the service is mlocked; the bytes zeroed after) and is `os.Link`ed to
    `/run/otc/wifi-join.request`, so a request is never overwritten and the PSK never touches the
    SD card (the buffer is sized for every byte escaped, so it never reallocates and leaves a copy).
    Logged at Info without the SSID (Debug only). `ApplyUpdate` refuses while a change is in flight
    (`wifi_changing`, `dev.update.wifi_changing`).
  - Root side: `scripts/device-runner/otc-wifi-join-runner.py` (Python stdlib, `#!/usr/bin/python3
    -I`, installed as `/usr/local/bin/otc-wifi-join-runner`), started by `otc-wifi-join.path`
    (`PathExists=/run/otc/wifi-join.request`) as `otc-wifi-join.service` (oneshot, root,
    `TimeoutStartSec=7min` - a slow change is about 5 - and `TimeoutStopSec=3min` for
    `ExecStopPost=... --stopped`; `StartLimitIntervalSec=0`, so a burst of requests never leaves the
    path unit failed until a reboot - the runner's own limit is the bound; `ProtectSystem=strict`,
    `ReadWritePaths=/run`, `AF_UNIX AF_NETLINK AF_INET AF_INET6`). It first removes what a run killed
    outright left in its directory (`req.*`, temp files), then renames the request into its own
    root-owned `/run/otc-wifi-join/` (0755), opens it `O_NOFOLLOW`, checks it is a regular file owned
    by `otc` with one link, reads at most 1025 bytes and refuses duplicate or unknown keys
    (`bad_request`, counted toward the limit too). Before touching the network: 10 runs in 15
    minutes of uptime -> `too_many`; an update asked for (`/var/lib/otc/update.request`, lstat
    only), `otc-update.service` active, or `/run/otc-update.lock` held (a non-blocking EXCLUSIVE flock
    released at once) -> `update_running`; `/run/otc-wifi.lock` (shared with the restart runner) not
    free within 10 s -> `busy`; no NetworkManager or wifi interface -> `no_wifi`; a scan
    (`--rescan yes`, `no` when refused; matched by SSID-HEX; a network not seen is looked for up to
    3 more times, 5 s apart, within 40 s - NetworkManager answers a rescan asked too soon with its
    old list, silently). The security comes from every access point with that name that the runner
    can join (WEP, 802.1X and OWE ones don't count): any secured one makes the network secured - an
    open copy of its name (anyone can broadcast one, louder) is never joined and the password never
    dropped - `wpa-psk` when one shows WPA1/WPA2, else `sae`; no password then ->
    `password_required`; a password for a name only open access points have -> `open_network`;
    none it can join -> `unsupported_security`. The previous connection is the one `activated` on
    the interface with the device connected (one still trying - an old password - is none). The
    trial profile is a keyfile written in `/run/NetworkManager/system-connections/` (created 0755 if
    missing, only where `/run/NetworkManager` exists; tmpfs: a wrong password never reaches the card,
    and the PSK is never on a command line) with autoconnect off, the SSID as decimal bytes,
    `cloned-mac-address=permanent`, `addr-gen-mode=stable-privacy` (a keyfile without it means EUI-64
    addresses from the MAC) and, for the same network, the IP settings and addr-gen-mode of the
    previous connection, or of an inactive `OTC-WiFi` for that SSID (the router's password changed
    and the device came back over a cable); `nmcli connection load`, `connection up --wait 60` on
    the interface (longer than NetworkManager's 45 s DHCP timeout), state 100 with an address, then
    the internet through THAT interface: `curl --interface IF` to the bridge host from `[otc]
    bridge-addr` in the root-owned config (any HTTP status), or without a bridge the default
    gateway answering a ping, each tried twice 3 s apart -> else `no_internet`. Each step rewrites
    `status.json` ("joining", the uptime now). Success: `connection modify` without `--temporary`
    saves it to `/etc` (checked: its FILENAME is no longer under `/run/`), `os.sync()`, `state.json`
    rewritten as `{"committed": NEW}` (no previous or trial left in it), then the old `OTC-WiFi`
    profiles deleted, other profiles of the same SSID set to autoconnect off (not deleted),
    `os.sync()` again. Failure: stderr classified (`Secrets were required` -> `wrong_password`, as
    NetworkManager 1.52 reports a wrong WPA2 password - and, for a profile that never connected, a
    drop during the handshake too; "supplicant disconnected" / "took too long to authenticate" ->
    `auth_failed`, not seen with 1.52; "could not be found" for a network the runner's own scan just
    saw -> `auth_failed`, as 1.52 reports a wrong WPA3 password, a refusing or silent access point
    that way; `not_found`, `timed_out`, `no_address`, else `join_failed`), the trial deleted and the
    previous connection brought back up. A run that finds a `state.json` left by one cut short (its
    `--stopped` killed too, as `systemctl kill` does) puts that right first (`put_right`), under
    the Wi-Fi lock. `--stopped` takes the Wi-Fi lock (10 s at most, then goes on), turns
    a status still `joining` into `interrupted`, deletes a trial only while its FILENAME is still
    under `/run/`, brings the previous connection up only when the interface isn't connected, and
    does nothing while the system is stopping; a change committed, or whose profile is already
    saved (cut short between the two), it finishes instead: old profiles retired, flushed,
    `connected`. At start (once NetworkManager answers) it removes stray `otc-wifi-*.nmconnection`
    files from `/run/NetworkManager/system-connections`, deleting a profile only while it is still
    that unsaved trial (that file, the trial's name, not active) - a saved profile only loses the
    stray file. It logs byte counts and codes, never the SSID or the PSK, and exception types only.
    `python3 scripts/device-runner/otc-wifi-join-runner.py --selftest` runs without NetworkManager
    (on the Mac too).
  - Status and texts: `/run/otc-wifi-join/status.json` (0644, written by temp file + rename:
    `{"v":1,"id","state","code","ssid","left","restored","uptime"}`) has no English. Go reads it with
    a 4 KiB cap, checks the id, state, code and SSID, and merges it with its own pending change: no
    status for that id after 60 s -> `no_answer` (the request file removed), `joining` more than
    600 s of uptime after its last word -> `interrupted` (never while the runner or its `--stopped`
    can still be at work; uptime, not the clock: no RTC, and NTP jumps it).
    `devtext.WifiJoinText(lang, code, ssid, left, restored)` renders `dev.status.wifi_join.<code>`,
    or, for the 8 codes that can come after the device left its network (`wrong_password`,
    `auth_failed`, `not_found`, `timed_out`, `no_address`, `no_internet`, `join_failed`,
    `interrupted`), `dev.status.wifi_join_back.<code>` (`left && restored`: "...back on its previous
    network") or `wifi_join_lost.<code>` (`left`, not restored: "...connect it to your router with an
    Ethernet cable"), from the key itself in every language, English included; an unknown code is
    `join_failed`. `WifiJoin.restored` is `left && restored`; the apps never branch on it and show
    `message`. The runner's `CODES` are devtext's `wifiJoinCodes` minus `no_answer` (Go's own):
    `TestWifiJoinRunnerCodes` compares them.
  - Rate limits: Go one change in flight and 6 accepted in 15 minutes; root 10 runs in 15 minutes
    (it holds even if the otc service is compromised); `ListWifiNetworks` relies on
    NetworkManager's own rescan throttling.
  - Accepted risk: additional users' instances run as the same uid `otc`, so a compromised one can
    write `/run/otc/wifi-join.request` itself and root's owner check can't tell it from the
    primary - as with the reboot and update requests. Root's validation and rate limit are the
    bound.
  - The bridge pool follows a network change (`websocket`'s network watcher): when the default
    route's interface or the local address changes (checked every 5 s), idle pool connections are
    closed, the dial backoff is reset and waiting retries are woken (nil-safe: `wake` is nil after a
    registration), so unplugging the cable doesn't leave the device off the bridge for minutes.
  - Apps (web `components/settings/WifiPanel.tsx` + `wifiFlow.ts`, iOS `WifiSection.swift`,
    Android `WifiSection` in `ui/settings/SettingsView.kt` + `WifiViewModel` + `WifiFlow.kt`; the
    same rules, tested row for row with the same case ids - N names, P passwords, C what is sent, K
    the cable-only confirmation, L/R on load, J JoinWifi's answer, W a look while waiting, K- which
    failures keep the picker open, U which listed networks can be chosen, T timing - in
    `web/scripts/wifi-flow-test.mjs`, `WifiFlowTests.swift` and `WifiFlowTest.kt`): a section just
    before Tailscale, for the primary's owner only, shown only for `managed`. The picker shows a
    lock for secured networks, "Open" for open ones (allowed: the reachability check protects),
    "Connected" for the current one, ones the device can't take greyed out as "Not supported"
    (unsupported security, a name with control characters or over 32 bytes), and "Other Network…"
    for a hidden name (a typed name is never changed: control characters are left to the device's
    `bad_name`); the confirmation says the device leaves its network for about a minute (or, on a
    cable with no Wi-Fi, that it stays reachable through it). A failed scan says so
    (`scan_failed`). After `wrong_password`, `auth_failed`, `not_found`, `password_required` or
    `open_network` the picker stays (or opens again) on the network tried. The password is sealed and never kept (no saved state, no autofill: web
    `autocomplete="off"` and a field not named "password", iOS no password content type, Android
    `importantForAutofill=no`); the name field has no auto-capitalisation, auto-correction or smart
    quotes. After `JoinWifi` they ask `GetWifiStatus` every 3 s (10 s a look, the bridge's own
    answers, errors and a moment's `managed=false` ignored) until `last_join` with that id ends,
    show "slow" after 240 s (in place of the spinner) and keep asking every 10 s; a reload, and a
    refusal (`busy`: the same request sent again), resume a `joining` change. A failed
    `GetInstanceRole`/`GetWifiStatus` leaves the section as it was.
  - Tests: `network`, `websocket` (the gates, sealed password, SetWifi, the update lock), `wifiwatch`,
    `devtext`, the runner's `--selftest`, and NetworkManager itself only in a NEW throwaway Lima VM
    with `mac80211_hwsim` and hostapd - never the `otc` VM's networking or a real device:
    `scripts/dev/wifi-join-hwsim.sh` (its header has the VM's setup; the access point lives in its
    own network namespace, bridged with a "cable" veth for the FAQ's way back). The real join on a
    Pi happens after the release, through Update.
- `devicerestart` — Settings > Restart device (release 115): `RestartDevice` (137) ->
  `RestartingDevice{expected_seconds}` (68), owner of the main instance only (`sup == nil` refused;
  older devices answer `unknown_payload`), and - as Shut down device, under
  `network.UnlessChanging`'s lock (`websocket/restart.go`) - refused while a Wi-Fi change
  (`wifi_changing`, `dev.device.restart_wifi_changing`) or an update (`updater.Pending`,
  `update_running`, `dev.device.restart_update_running`) runs: a restart in the minutes after an
  update restarted the service, while update.sh checks that the new release starts, left a release
  that doesn't start installed with its rollback never run. Writes `/var/lib/otc/reboot.request`;
  `otc-reboot.path` runs `otc-reboot-runner` (removes the request first, refuses in the first 2
  minutes after boot - the device too -, drops it while an update was asked for or runs and while
  the Wi-Fi lock is held, which it then keeps until the machine goes down, as the shutdown runner;
  `--dry-run` by hand; waits 3 s for the answer to leave, `systemctl reboot`). The apps show the
  device's words for a refusal (`TestRestartDevice`; devicerestart's
  `TestPowerRunnersRefuseDuringAnUpdateOrAWifiChange` runs both runners on Linux). In
  the Device version section of web `UpdatePanel`, iOS `UpdateSection`, Android `UpdateViewModel`:
  confirm, "Restarting…", then a `GetInstanceRole` every 5 s from 20 s on; back = the device's own
  answer (never the bridge's `device_unreachable` Ack) after a failed look or 90 s; "slow" after 5
  minutes. Settings > Shut down device (issue #202, release 164; `shutdown.go`): `ShutDownDevice`
  (160) -> the generic Ack, the owner of the main instance only (`sup == nil` refused), refused in a
  container (`dev.device.shutdown_in_container`, code `not_in_container`: the server stops it) and on
  a demo device, and - under `network.UnlessChanging`'s lock, as ApplyUpdate - while a Wi-Fi change
  (`wifi_changing`) or an update (`updater.Pending`, `update_running`) runs
  (`websocket/shutdown.go`); older devices answer `unknown_payload`. `devicerestart.ShutDown` writes
  `/var/lib/otc/poweroff.request` (`dev.device.shutdown_needs_update` without the runner,
  `shutdown_too_soon` in the first 2 minutes after boot); `otc-poweroff.path` runs
  `otc-poweroff-runner` (`scripts/device-runner/otc-poweroff-runner.sh`: removes the request first,
  drops it in the first 2 minutes after boot - a request left behind must never switch a device off
  as it starts -, while an update was asked for or runs - the update request, `otc-update.service`
  active, `/run/otc-update.lock` held - or while `/run/otc-wifi.lock` is held, which it then keeps
  until the machine goes down; waits 3 s for the answer to leave, `systemctl poweroff`, which stops
  otc and MariaDB cleanly; `--dry-run` by hand says what it would do). The device then stays off
  until it is unplugged and plugged in again. Web: Shut Down Device beside Restart Device in
  `UpdatePanel` (not when `RespUpdateInfo.container`), a `ConfirmDialog` (Cancel first; the device's
  refusal in it, `unknown_payload` -> `web.settings.shutdown.needs_update`), then "Shutting down…
  Once its lights stop blinking you can unplug it." (`web.settings.shutdown.done`). After the Ack
  every app leaves the device alone for 90 s: no reconnect, the note stays, no unreachable screen
  over it; then everything as usual, and signing out drops it. On the web `net/shutDownHold.ts`:
  `useWS.request` opens no new socket meanwhile (a `NoAnswer` in the note's words; a socket still
  open carries on), App ignores the bridge's `device_unreachable` and hides the "Reconnecting…"
  pill (`components/settings/shutDown.ts` for the answer, `web/scripts/shut-down-test.mjs`). iOS
  `UpdateSection` and Android `UpdateViewModel` follow the same rules. Tested on the `otc-2gb` Lima
  VM (2026-11-03, installed by install.sh): update.sh's runner block, run on it with the runner
  removed and a request left behind, installed and enabled it and removed the request first; the
  dry run dropped a request while update.sh's lock or the Wi-Fi lock was held; a request written as
  the otc user powered the VM off, otc and MariaDB stopping cleanly first; one written 8 s after
  boot was dropped.
- `images_tagger` — runs the RAM++ ONNX model (paths from `[tagger]` config) to auto-tag photos;
  requires CGO + libonnxruntime at runtime (see Build section). The model file is 873 MB (int8,
  `ram_plus_int8.onnx`); measured with ONNX Runtime 1.24.3 on arm64 (macOS, 2026-10-08) it is
  ~0.85-1.0 GB resident when freed memory goes back to the system (~2.3 GB with what the
  allocator keeps of the load), ~1.4 GB while loading, and a run needs ~650 MB more - which the
  default CPU arena keeps after it (three runs at once: +1.3 GB), so every device has run it
  without the arena since release 176. Loading reads the IO names from a
  whole temporary session (`firstTensorIO`), cached per process (`cachedTensorIO`) for reloads.
  Outside low-memory mode the primary loads it in the background (`files_manager/tagger_load.go`)
  the first time a photo of any of its users needs tags (`startTagger`, from `waitForTagger`; at
  start until release 158), so a device whose users all have Image Tagging off never holds it;
  a load that fails no longer ends the process (`log.Fatal`, then a restart every 3 s, the device
  unreachable for the Update that would repair it): one Alert (`dev.alert.tagger.not_loaded`), photos
  and videos get their place tags only (`retryTagger` answers `modelserver.ErrNoTagger`, which
  processing skips quietly), and it is tried again 1, 2, 4... minutes later, an hour apart at most;
  Reprocess Media adds the missing tags.
- `images_embedder` — Smart search's model: Google's SigLIP 2 base (patch 16, 256 px,
  Apache-2.0) as two ONNX towers, made by `models/siglip2_export.py` from the safetensors weights
  and checked against transformers' fp32 model (cosine >= 0.99, or it fails). Image tower: 8-bit
  weights in blocks of 32 (MatMulNBits, int8 compute; cosine 0.9997 - dynamic int8 fell to 0.90),
  ~114 MB; text tower: the 256k-token table int8 with a scale per token, looked up as
  Gather(int8) -> Cast -> Mul(Gather(scales)) so ONNX Runtime can't fold it back to fp32, and the
  weights as above, ~296 MB; each with its weights in a `.data` file ONNX Runtime memory-maps.
  `ImageEncoder` (`Resize`: squeezed to 256x256 by `draw.BiLinear`, PIL's averaging bilinear,
  scaled to -1..1; `EmbedResized` for a picture already resized), `TextEncoder` + `Tokenizer`
  (github.com/eliben/go-sentencepiece on the Gemma tokenizer that ships with SigLIP 2: text
  lowercased, at most 63 tokens, `<eos>`, padded to 64 - id for id with transformers on 40 texts in
  the seven languages, `testdata/tokens.json`); vectors L2-normalised, 768 numbers. ONNX Runtime
  through `imagestagger.InitRuntime`. Measured on the demo library (M-series Mac): a photo 58 ms
  with resizing (RAM++ 335 ms), a query 10 ms; precision of the first results 0.96 English, 0.93
  Spanish, 0.90 the others, against 0.87 for RAM++'s best tags. Large (patch 16) did worse here at
  2.6x the time and 2.4x the download. The files aren't fetched by the scripts since release 152:
  the device downloads them once Smart search is first turned on (`modelfetch`, Smart search above).
- `modelfetch` — a model's pinned files downloaded on demand, in the background (Smart search's,
  release 152): size and SHA-256 per file, `.part` files resumed with `Range` and renamed only once
  they match, a speed cap, a free-space check, a stall cut, retries from 1 minute doubling to an
  hour while `Wanted`, `Stop` removing the parts; `State` (ready, downloading, done/total, the last
  `Problem`). Its clock and waits are injectable for the tests.
- `images_quality` — On this day's photo quality: NIMA (Google's Neural Image Assessment), idealo's
  MobileNet aesthetic weights trained on AVA (Apache-2.0), exported to ONNX
  (`nima_mobilenet_aesthetic.onnx`, 12.8 MB, sha256 `b35a4d66…71d1`; matches Keras to 1e-6), plus
  pure-Go `Sharpness`, `Exposure` and `DHash`. Input: the big thumbnail resized nearest-neighbour to
  224x224 (`Resize`, as trained: an antialiased resize ranked photos differently, Spearman 0.77),
  scaled to -1..1, NHWC; output the mean of the 10 rating bins. One thread, ~12 ms a photo on an
  M-series Mac. `[quality] model-path` (install.sh, the container, 150.sh adds the section); the
  file is optional: fetched with `fetch_optional` (install.sh), by update.sh at every update while
  missing, and by the container at each start, from the `models` GitHub release; without it photos
  are measured without the score. The primary loads it at start (`loadQualityModel`; low-memory: in
  `idleModels`, at first use, released when idle) and serves it to the children
  (`modelserver.ServeModels`, op "aesthetic" with the 224x224 pixels, `HasAesthetic` in "info"; an
  older primary's "unknown request" is `ErrNoAesthetic`). `imagestagger.InitRuntime` initializes
  ONNX Runtime once for both models. `TestQualityModelPinIsTheSameEverywhere` checks the pin in
  install.sh, update.sh and the entrypoint.
- **A screen on the device** (release 171, `scripts/screen-runner/`; the web half is the web app's
  screen mode, `?screen=1`): a screen on the Pi's HDMI (or a DSI panel) shows the web app full-screen.
  - Root watcher `otc-screen-watch.py` (Python stdlib, `/usr/local/bin/otc-screen-watch`,
    `otc-screen.service`, enabled): every 3 s reads `/sys/class/drm/card*-{HDMI-A,DSI}-*/status`.
    A screen `connected`: installs the browser the first time (`apt-get install
    --no-install-recommends cage chromium kbd wlr-randr fonts-dejavu-core fonts-noto-color-emoji`, then
    `apt-get clean`; not while `otc-update.service` runs, at least 1.5 GB free, a failure tried again
    after 30 min, said on tty1), makes the `otc-screen` system user (home `/var/lib/otc-screen`,
    groups video/render/input where they exist), writes `/run/otc-screen/browser.env`
    (`OTC_SCREEN_URL=http://127.0.0.1:<[otc-api] port of /etc/otc_dev.ini>/?screen=1` and
    `OTC_SCREEN_SCALE`: `screen_scale(edid)`, the first detailed timing's width over its mm, at 132
    CSS px per inch in quarters from 1 to 3 - 2 on a 9.7-inch 2048 x 1536, 1 without a size) and the
    managed policy `/etc/chromium/policies/managed/otc-screen.json` (`policy()`: URLBlocklist `*`
    with only the device's origin allowed - navigations only, the map's tiles still load - no
    downloads, no file chooser, no password manager/autofill/translate/sign-in/devtools/printing),
    then `systemctl start --no-block otc-screen-browser.service`. No screen for 15 s (or a running
    `display-manager.service`, checked every 60 s): stops it. While nothing is connected it writes
    `detect` to the disconnected HDMI outputs every 10 s - the Pi probes once at boot and missed a
    portable screen still waking up (no picture until a restart); never to a connected one (it would
    clear a `video=...D` force). It exits at once off a Raspberry Pi (`/proc/device-tree/model`) or
    under 3000 MB of MemTotal (the 2 GB tier). `--selftest` runs on the Mac
    (`TestScreenWatcherSelftest`).
  - Browser `otc-screen-browser.service` (no `[Install]`): `User=otc-screen`, `PAMName=login` on
    `TTYPath=/dev/tty7` with `XDG_SESSION_CLASS=user` - logind makes it an active seat0 session, so
    cage gets the DRM device and the touch screen (checked in a systemd container: `loginctl` shows
    the session on seat0, class user, tty7). `ExecStartPre=+... --wait` (root) waits for any HTTP
    answer from the web app with the console still on screen, then `chvt 7`; `ExecStart` is
    `otc-screen-browser.sh`: clears Chromium's crash flags, `exec cage -d -s -- <itself> --app`, which
    sets the scale on every output (`wlr-randr --output X --scale S`) and `exec chromium --kiosk
    --ozone-platform=wayland ...` (no swipe back, no pinch zoom, touch events on). Never
    `--force-device-scale-factor`: on Wayland it shrank the window to a quarter of the screen with the
    page unscaled (tried in the container; the output scale gave a full-screen page at 2x). Restart=always;
    `ExecStopPost` `chvt 1`. The launcher refuses any OTC_SCREEN_URL but
    `http://127.0.0.1:<port>/?screen=1` (`TestScreenBrowserTakesOnlyTheDevicesAddress`). Ctrl+Alt+F1
    is the console. On Raspberry Pi OS trixie both come from the Raspberry Pi archive's main component
    (chromium 156, cage 0.3.1 on 2026-11-06), which the daily security updates take; ~600 MB on disk,
    ~475 MB PSS.
  - Installed by install.sh and by every update.sh from 171 on (its own update.sh the first time):
    the same five files, `TestScreenRunnerInstalledTheSameEverywhere` - the fifth (release 173) is
    `otc-screen.conf` in `/etc/tmpfiles.d/`, `d /run/otc-screen-key 0700 otc otc -`, made at once by
    `systemd-tmpfiles --create` and at every boot. A cage/chromium headless run
    (WLR_BACKENDS=headless, pixman) in an arm64 trixie container with the Raspberry Pi archive showed
    the kiosk and the policy's "This page is blocked" for another site.
  - The screen stays signed in (release 173; owner, 2026-11-07: "the login shouldn't expire, the
    idea is to have a frame mode"): a **screen key** (`session/screenkey.go`,
    `websocket/screen_key.go`). The 1-hour session tokens are memory only, so every update (a
    service restart) and every browser restart more than an hour after the last refresh signed the
    wall screen out. `ReqIssueSessionToken.screen` (1) asks for one and `ReqAuthWithToken.screen` (2)
    signs in with it; both only on a connection `localScreen` takes for the device's own browser
    (`connHandler.screen`, set at construction: not a container, not a user's instance, peer
    loopback, no proxy headers - tailscaled comes from loopback with them -, Origin
    `http://127.0.0.1:<port>`/localhost/`[::1]` equal to the Host), else an error / refused. The key
    is 32 random bytes the browser keeps (`otc_screen_key` in localStorage, Chromium's profile on
    the SD card); the device keeps the session's data key (`Session.dataKey`, the vault secret's
    SHA-256 the files are sealed with), its uuid and the SHA-256 of the vault's sealed secret,
    sealed with AES-256-GCM under that key, in `/run/otc-screen-key/key` (0600, temp + rename) -
    in memory only (swap is zram), so a device switched off keeps nothing that opens anything and
    issue #156's promise holds. One per device, the latest replacing the one before; not used up by
    signing in, no expiry (`expires_at_unix_ms` 0). It ends with a sign-out on the screen
    (`ReqRevokeSessionToken` from it, `RevokeScreenKey`), a password change (`PasswordChanged`
    removes the file under `tokenMu`, and `IssueScreenKey` writes under it with the generation
    checked), a vault that isn't the one it was issued with (a reset, a rewrap) or a reboot. A vault
    that can't be read when it is redeemed is answered `storage_unavailable`, no verdict on the key.
    No directory (a container, a device without the runner) is `ErrNoScreenKeys`: the screen keeps
    session tokens. A device from before ignores the field and answers a session token, which the
    web keeps as one. Tests: `session/screenkey_test.go`, `websocket/screen_key_test.go`,
    `web/scripts/screen-key-test.mjs`.
- **Low-memory profile** (`files_manager/lowmem.go`): MemTotal under 6 GB (a 4 GB Pi 5 reports
  ~3.7 GB) or `[otc] low-memory = on` (`auto` default, `off`; the primary passes a configured
  choice to its supervised instances as `OTC_LOW_MEMORY`). Logged once at start ("low-memory mode:
  4 GB of memory ..."); a device with 8 GB and no setting logs and does nothing new. One media job
  at a time (`processingGuard`'s slot, taken through `beginProcessing(hash, kind)` by the lanes'
  jobs, the backfill and Reprocess - which take it themselves before `reprocessOneFile` - a
  thumbnail fix right before its decode, and - in low-memory mode only - a post's transcode), Go's soft limit 20% of MemTotal
  instead of 40% (`setMemoryLimit`, after `cfg.Init` now), content budget MemTotal/16 (the 256 MB
  floor on 4 GB), ffmpeg with `-threads 2` (`ffmpegInput` for frames and transcodes, plus the
  encoder's; a 4K HEVC frame took ~680 MB with ffmpeg's own thread count on 16 cores, ~270 MB with
  1-2). RAM++ runs without ONNX Runtime's CPU arena and
  memory pattern (`imagestagger.LoadOptions{NoArena}`), loads at first use (`waitForTagger` loads it
  before a photo's 10 s deadline starts) and is released with the face models after 10 idle minutes
  (`idleModels`, which also runs one model call at a time - tags or faces, local or a supervised
  instance's; `serveModels` = `modelserver.ServeLimited(..., 1)` reads one child's pixels at a time,
  "info" never waits). Face detection is unchanged (YuNet at 1600 px keeps ~100 MB; at 1024 px, or
  cropping from a 2x-downscaled photo, a group photo lost 2 of 8 faces). Each model load logs what
  the process holds after it.
- **Memory on every device** (release 176; Cala, 2026-11-08: an 8 GB Pi with a screen attached was
  killed by the kernel every few hours while a phone uploaded, the process holding up to 6 GB with
  under 300 MB left, each time 15-40 minutes after the hour of one job at a time ended):
  - Freed native memory goes back. `hardening.LockMemory` first sets glibc's mmap threshold at 1 MiB
    and 2 arenas (`ReturnFreedMemory`, cgo `mallopt`), on every device that locks its memory - until
    176 only in low-memory mode. Linux refuses MADV_DONTNEED on mlocked pages, so what ONNX Runtime,
    OpenCV and the HEIC decoder freed stayed resident, and glibc's threshold for a block of its own
    rises to 32 MB after large frees; a block in its own mapping is unmapped when freed, locked or
    not. Go's own scavenger falls back to mmap and is fine.
  - RAM++ runs without ONNX Runtime's CPU arena and memory pattern on every device
    (`loadRAMTagger`, `LoadOptions{NoArena}`, as low-memory mode always did): the arena kept each
    run's ~650 MB after it, ~1.3 GB for the three analysis workers, held for good.
  - Memory admission (`files_manager/memadmit.go`): every `beginProcessing` job (the lanes, the
    backfills, Reprocess, thumbnail fixes, transcodes and cuts) starts beside others only while the
    device has `cJobHeadroom` (1.5 GB) available for it, counting what the jobs let in during the
    last `cAdmitSettle` (2 s) take, and otherwise waits for one to end or for the memory, looking
    every second (`memAdmission`; Cancel ends a Reprocess's wait). A job with nothing else running
    always starts. Available is MemAvailable, and in a container no more than its limit less its use
    (`container.MemoryAvailable`); unknown (off Linux) waits for nothing. A wait is logged at most
    every 10 minutes ("processing: N MB available, under the 1536 MB a job needs ..."). The lanes'
    worker counts stay as measured on a Pi doing nothing else; what else runs - the screen's
    Chromium (~0.5-1 GB), MariaDB, other users' instances - now lowers how many run at once.
  - After each job a new resident-memory high-water mark (128 MB steps) is logged on every device
    ("memory high-water mark: this process holds ... MB, ... MB available on the device" - in a
    container, what is left under its limit).
  - Measured on 2026-11-08 in the release image, held to 7.9 GB with 2.3 GB of it taken by a file
    in its RAM disk (Cala's screen and system), memory locked, 4 CPUs, Smart search, tagging and
    faces on: 60 12 MP and 12 48 MP HEICs and three 45 s 4K HEVC videos sent as the iPhone sends
    them (three at once, 4 MB chunks). Release 175: the process at 3.98 GB at its peak, 3.72 GB at
    rest after it, 3 min 13 s; release 176: 2.36 GB and 1.94 GB, 3 min 53 s (giving memory back
    costs some speed). glibc's setting alone on 175 (`GLIBC_TUNABLES`): 3.62 GB at rest.
  - Tests: `memadmit_test.go`.
- **Run marker** (`files_manager/runstate.go`): `.otc-run` in the instance's working directory
  (the StateDirectory, or the user's home - not the RuntimeDirectory, which systemd empties at every
  stop), written at start with the boot id and the OOM kill counters (`/proc/vmstat` `oom_kill`,
  the cgroup's `memory.events`), refreshed when they move (a look every minute and at each
  in-flight write, so another program's OOM kill long ago isn't taken for this run's). bin/otc.go
  catches SIGTERM before `filesmanager.Init` writes it, and `stopServices` removes it first
  (`Manager.Stopped()`), except when the cgroup's oom_kill rose during the run: systemd's default
  `OOMPolicy=stop` then stops the whole unit after the kernel killed one of its processes (a
  supervised instance, an ffmpeg), and the marker stays, `stopped_oom`. Found at a start, the
  previous run died: same boot and a counter higher than at the last look (or `stopped_oom`) = out
  of memory - one Alert per series (`dao.UpsertErrorNotification`, kept by id in the marker: "Your
  device ran out of memory and restarted 12 times since 19:34", detail "This device has 4 GB of
  memory, ..."; never a push). Other deaths are only logged, another boot (a power cut) counts
  nothing. A series is deaths of runs younger than an hour. The run after a death goes one job at a
  time and records the hash in flight (rewritten in place, padded, never truncated - ext4 flushes
  a truncated-and-rewritten file at once) for an hour (`recoveryFor`; a low-memory device always
  does). Its analysis, backfill and Reprocess (Cancel ends that wait) - never an upload's
  thumbnail - wait 1, 2, 4, 8, 16, then 30 minutes, only when the death looked like processing's
  (out of memory, or content in flight), and a death during the dead run's own pause doesn't make
  it longer (`level`, `resume_at`). Content in flight at two deaths gets set aside, unless a model
  was loading (`loading`, set by `idleModels.load`): no strike for that. Set aside
  (`files_manager/setaside.go`): its `pending_analysis` row goes, it gets the backfill's
  `.nothumb` marker, it is listed in `<storage>/.set-aside` with when and the binary
  (`binaryID`: size and mtime) - skipped by Reprocess and the thumbnail fixes - and gets an Alert
  of its own ("<name> was set aside"). `retrySetAside` at start gives it back (pending again, the
  marker removed) under another binary or after 30 days. The memory Alert and set-aside rows are
  standalone: `actor_domain = '(standalone)'`, which `AddErrorNotification`'s grouping skips (it
  joins only rows with an empty one). The unit keeps systemd's default `OOMPolicy=stop`;
  `OOMPolicy=continue` (an ffmpeg or a supervised instance killed without restarting everything)
  would need install.sh and a release script.
- `modelserver` — issue #167: the primary instance loads RAM++ and the face models once and
  serves them on `models.sock` in its working directory (0600, Unix socket); the supervisor sets
  `OTC_MODELS_SOCKET` on each child, which then uses `modelserver.Client` for
  `files_manager`'s `Tagger`/`FaceDetector` instead of loading its own copy (~1-2.3 GB resident).
  Protocol v1
  (`cProto`, reported by "info"): a gob header, then raw RGBA pixels (`request.N` = 4*W*H, checked
  under 2 GiB before allocating); tags use `tags-resized` with only the 384x384 image the child
  scaled itself (`imagestagger.Resize`, then `TagsResized`), faces still get full resolution. A
  child speaks v1 only when "info" reports it, and the primary still accepts v0 (pixels inside the
  gob message) - keep both when changing the socket. The primary reads and answers two requests at
  a time (`cServedAtOnce`; one in low-memory mode): a face request carries the whole photo. With no
  tagging model loaded the answer is `ErrNoTagger`, which reaches a child as itself. Release 152:
  "embed-fetch" (`Who`, `Want`: a child asking for Smart search's model, or no longer) and
  `EmbedderFetch` (`EmbedderState{CanFetch, Downloading, Percent, Problem, Need, Left}`) in its
  answer and in "info" - new gob fields, which either side ignores when it is older; an older
  primary's "unknown request embed-fetch" is `ErrNoEmbedder`. "info" and "embed-fetch" never wait
  for a turn.
- `supervisor` — issue #82's users' instances, run by the primary. One that keeps stopping is started
  again after 4 s, doubling to 10 minutes (`respawnBackoff`; a run of 10 minutes or more starts a
  new series), and the 5th stop in a row is alerted, once a series (`dev.alert.user.keeps_stopping`);
  Stop ends a pause at once. Its ini, on the SD card, is rewritten only when it changes
  (`writeIfChanged`).
- `face_recognition` — (matching, issue #173: every face row is kept, but new faces are matched
  against at most 20 decrypted *reference* embeddings per person cached in memory -
  `files_manager/face_refs.go`; at 20 an outlier isn't added and the most redundant reference is
  dropped, so the set stays varied; the cover medoid is over the references;
  `InvalidateFaceRefs` after Reprocess, person delete/merge) issue #52's "People" search: detects faces (YuNet) and embeds them (SFace)
  via `gocv`, humans only. `files_manager.processFaces` (called from the slow processing lane) gates this on `settings.face_recognition_enabled` (off unless the owner turns it on - in Settings or the setup wizard; release 43 made the column default 0 again, issue #178, since faces are biometric data) checked *at upload
  time* - enabling it later never retroactively processes anything already in the library, by
  design (see the `faces` table's doc comment in `db.sql`). Requires CGO + a real OpenCV install at
  build time (see Build section); optional at runtime like APNs - a device with `[faces]`
  unconfigured just has the feature unavailable, nothing else affected. On Linux each model is
  probed first (`model_probe_linux.cpp`), so a bad or empty model leaves faces off instead of
  aborting. A hash's faces go with its last file or version (`dropFacesOfHash` ->
  `dao.DelFacesByHash`, which also deletes unnamed people left with no face); lock order is always
  the hash lock, then `outOfImagesMu` (issue #192), then `faceRefsMu`, and `processFaces` never takes a hash lock. Only the people who
  lost a reference are reloaded (`faceRefsStale`), the whole set only after a database error.
  People's "Not this person" / "Someone else…" (`RemoveFromPerson`, envelope 146): the faces of
  `person_id` in the photos named (`hashes`, at most 10000, `websocket/people.go`) move to
  `to_person_id`, or to no one - `faces.person_id` `""`. No one's faces are counted, shown as a cover,
  searched by (`SearchMedia`'s `<> ''`) or matched through by nobody (`ListFaceEmbeddings` skips them,
  `faceRefs.add` and `matchOrNewPerson` never take `""`), yet the row keeps `HashHasFaces` true, so the
  photo isn't analysed again; they go with their content like any face, and a Reprocess finds every
  face afresh. `dao.RemoveFromPerson` is one transaction (the target locked `for update`, gone ->
  `ErrNoSuchPerson` -> `dev.files.person_gone`): covers among the moved faces are cleared and an
  unnamed source left with no face is deleted (a named one stays); `filesmanager.RemoveFromPerson`
  (under `faceRefsMu`) then rebuilds both people's references from their own faces, works their
  covers out again and calls `noteDelete` (the apps drop the photos from the grid and lower `have`).
  `DeletePerson("")` and `MergePeople` never touch no one's faces. The demo refuses it.
  A photo's faces, for the viewer's Info (release 159; owner, 2026-11-01, from a user: "see what
  face it has, especially on new images ... and be able to remove faces from there"):
  `GetPhotoFaces{path}` (req 156) -> `PhotoFaces` (resp 75: `faces` left to right - person id,
  name, `""` while unnamed, the crop decrypted, the box in the photo's pixels - `face_recognition`,
  and the photo's `hash`); none while face recognition is off; no one's faces aren't listed
  (`dao.FacesInPhoto`, `filesmanager.PhotoFaces`). A face's × in Info is `RemoveFromPerson` with its
  person and that hash (both faces when one person has two in the photo). The demo allows the list.
  People added by hand (release 179; owner, 2026-11-09: "add people to the photos and videos by hand
  ... select one of the faces that are not already in the photo"): `AddToPerson{person_id, hashes}`
  (req 170, Ack; refused before any read by `websocket/people.go`'s `addToPersonRefusal` - no person,
  no hash, more than 10000, face recognition off - then `dev.files.person_gone`, or
  `dev.files.person_add_gone` when none is a photo or video a file holds). Rows of `people_by_hand`
  (db.sql: `hash`, `person_id`, `created`, PK (`person_id`, `hash`), key `hash`; `updates/179.sh` and
  install.sh's release 179 block, `updater/release179_test.go`), never `faces` rows: no box, crop,
  embedding or quality, so matching, covers, medoids, the looks pass and `HashHasFaces` never see one.
  `dao.AddToPerson` (the person locked `for update`) adds a row per distinct hash a `files` row holds
  as image/* or video/*, but where the person has a face, is already, or the content is kept out of
  Images (`skipped_analysis`: no people, as no faces). A row makes the content theirs wherever a face
  does (`dao/people_by_hand.go`): the person search (`searchMediaOnlyClauses`' union, `estimateMedia`'s
  count and driver, `personIn` in `rowClauses` and `RankMedia`), People's `face_count`, `FacesOfHashes`
  (the memories, hiding a person) and Info (`PeopleByHandIn`: after the faces, in the order added,
  `by_hand`, x/y/w/h 0, the crop `personCoverSQL`, ListPeople's own; a person with a face there isn't
  listed twice; `adds_people` always set; a video answered too). They follow the content and the
  person: `RemoveFromPerson` takes them off (to "") or moves them (`moveByHand`: dropped where the
  other person is already, by hand or by a face) and counts them apart from faces, so
  `filesmanager.RemoveFromPerson` rebuilds no references when only rows moved; `MergePeople` moves them
  so; `DeletePerson` and `WipeTagsAndFaces` delete them; `DelFacesByHash`/`DelFacesByHashes` (content
  gone, kept out of Images - not given back when shown again) and the orphan sweep delete a content's;
  an unnamed person goes only with no face and no row (`cNoOtherRefs`); a face of the person stored in
  the content (`AddFace`, `CopyFaces`, the moves: `dropByHandUnderFaces`) takes the row's place; an
  edit, cut or compress copies them to the new content (`CopyPeopleByHand` in `saveEdit`; the original
  keeps its own) and a revert copies the edit's to the original (one added after the edit isn't lost).
  Face recognition turned off keeps them, as it keeps faces. A database without the table reads as no
  one added (`cByHand` -> `byHandNone`, an empty derived table of `faces`' columns, by `byHandQuery`),
  deletes nothing, and `AddToPerson` is `ErrSchemaBehind`. Under `faceRefsMu`, `memories.changed()`
  after a change; the demo refuses it. Tests: `dao/people_by_hand_test.go`,
  `dao/people_by_hand_mysql_test.go` (`OTC_TEST_MYSQL_DSN`), `files_manager/people_by_hand_mysql_test.go`
  (`OTC_TEST_DEVICE_MYSQL_DSN`: an edit, a revert, a cut), `files_manager/person_faces_test.go`,
  `websocket/people_test.go`, `websocket/photo_faces_test.go`.
  Issue #181 (release 74): image tagging has the same kind of switch, `settings.image_tagging_enabled`
  (`SetImageTaggingEnabled`, read per file in the slow lane by `imageTaggingEnabled`); when off
  only the place tags from a file's own location data are written. Web, iOS and Android show it
  under Face Recognition. What a device's memory allows (owner, 2026-11-01: "the 4 gigabytes is
  going to don't have tagging, and the 2 gigabytes is not going to have tagging and users"; 8 GB
  everything; then 2026-11-03, issue #209 from a user with a 4 GB Pi 4, "Opt-in on 4 GB"), in
  `lowmem.go`: `MemoryProfile.TaggingAllowed` - outside low-memory mode always, in it from
  `cTaggingBelow` (3 GB) or of unknown size -, `TaggingHeavy` - allowed and low-memory: a 4 GB
  board, or `[otc] low-memory = on` - and `ExtraUsersAllowed` - at least `cUsersBelow`, 3 GB,
  between the 2 and 4 GB boards. Every instance's `Init` (`applyTaggingDefault`,
  `tagging_default.go`) then: under 3 GB turns Image Tagging off (`TurnOffImageTagging`, chosen or
  not) - `imageTaggingEnabled` is false whatever the setting, `Settings.image_tagging_available`
  (12) false and `image_tagging_enabled` false, `SetImageTaggingEnabled` on refused
  (`tagging_unavailable`, `dev.device.tagging_need_memory`: "at least 4 GB"), the apps show the
  switch off and disabled, the footer/hint ending "It needs a device with at least 4 GB of memory";
  heavy, applies the default off (`ApplyImageTaggingDefault(false)`) unless
  `settings.image_tagging_chosen`, so the user turns it on and it stays on through restarts, and
  `Settings.image_tagging_heavy` (13) is true, the apps showing under the switch "On this device
  Image Tagging uses about 1-2 GB more memory, and new photos take longer to process."
  (`web.settings.tagging.heavy`); elsewhere the default on (`ApplyImageTaggingDefault(true)`) unless
  chosen. Releases 160-163 turned it off at every start on a 4 GB device too, leaving
  `image_tagging_chosen` as it was, so those libraries stay off - chosen or not - until their user
  turns it on (`TestImageTaggingDefaultMySQL`'s release 160 case). In low-memory mode RAM++ keeps
  its handling (loaded at first use, NoArena, released after 10 idle minutes, one model call at a
  time). A 2 GB device takes no other users: `RespUsers.can_add` (3) false in ListUsers, and
  `CreateUser` refused before anything is provisioned (`users_unavailable`, `dev.users.need_memory`);
  the apps show `*.settings.users.need_memory` in place of Add user (and the bridge-access switch);
  existing users keep working (`websocket/memory_tiers.go`, `deviceMemory` for the tests). Unset
  fields mean available and nothing to say (older devices). The apps' rules: web
  `components/settings/memoryTiers.ts` (`taggingSwitch`'s `heavy`; `web/scripts/memory-tiers-test.mjs`),
  iOS `MemoryTiers.swift` (`MemoryTiersTests.swift`), Android
  `DeviceSettingsViewModel.State.withSettings` / `UsersManagementViewModel.State.withUsers`
  (`MemoryTiersTest.kt`); a refusal shows the device's words; the tour's Users step rings the note. Release 158 had made tagging's
  default follow the memory (owner: "perhaps we can disable tagging to run in small devices"):
  `updates/158.sh` (and install.sh) add `settings.image_tagging_chosen` once, marking a switch
  already off as chosen (the old default was on), and never again; `SetImageTaggingEnabled` sets it.
  Measured on 2026-11-01
  in the release 157 container held to 3.5 GB, 4 CPUs (`--cpuset-cpus`), the demo library plus
  four 48 MP JPEGs, two 48 MP HEICs and a 30 s 4K HEVC video, faces and Smart search on, the
  cgroup's `memory.peak` and `memory.stat` sampled every 5 s: tagging off 1.37 GB at most (~0.5 GB
  of it file cache), 0.86 GB anonymous, processed in ~45 s; tagging on 2.55 GB, 1.77 GB, ~3 min 10 s;
  held to 1.7 GB with tagging off 1.25 GB, 0.65 GB, everything but the HEICs (over the 42 MP decode
  limit there).
- `bg_processor` — background job runner invoked from `files_manager`/`websocket`.
- `websocket` — the `/ws` connection handler and dispatch switch described above; also owns
  `ensureBridgePool()`/`openBridgeConn()`, which the device uses to dial *out* to the bridge relay
  (`[otc] bridge-addr`) so the bridge can reach an otherwise unreachable home device. The pool is
  self-managing (5 ready, refilling in batches of 2 once it dips to 3) rather than a fixed count
  dialed once at startup — see `ensureBridgePool`'s doc comment. On those connections the requests
  running 10 s or more are named to the bridge every 20 s (`bridge_busy.go`: a pong carrying
  `BusyPrefix` and the ids), so it waits for a long one instead of answering "unreachable" at 90 s. Issue #170: a pooled connection stops counting as available at its first relayed message (the bridge keeps it for the client's whole session), and the pool refills right then - `serveConnection`'s `onFirst`.
  The pool's `pending` counts dials in flight *and* retries waiting out a backoff; a successful
  registration ends every backoff (`bridgePool.wake`). Replies go through `writeReply` (stall
  deadline `cReplyStall`, 1 min, renewed per 64 KiB piece) and `marshalReply` (a marshal error sends
  a clone with invalid UTF-8 replaced). `reserveMemory` reserves only for a caller allowed to make
  the request (`GetMediaRange` 2x its span for a known token, `GetStaticAsset` 2x the file, too).
  Once the peer is gone, queued `pureDownload` requests are skipped (`GetMediaRange` is one) - never
  add one with side effects (`DownloadSharedLink` counts opens). Until a session or a friend exists
  (`anonymous`), a connection has `cMaxInFlightAnon` (4) requests at once - the next isn't read
  meanwhile, nothing is refused - and its messages are read with `wsframe.ReadFree`, 64 KiB free
  (`cAnonFrameFree`), the rest in `anonFrameBudget` (64 MiB, reserved by capacity); then 32 and
  `frameBudget` past 4 MiB (`frameRules`). A session that never signs in nor authenticates as a
  friend ends once idle (`anon_sessions.go`, `anonWatch`: no request read and none being answered
  for `cAnonIdleDirect`, 2 minutes, on the device's own listener; `cAnonIdleBridged`, 15, on a bridge
  connection, counted from its first relayed message - one idle in the pool waits for a client by
  design): gorilla's Upgrade clears every deadline and the device never pings, so a socket that sent
  nothing used to live as long as its TCP connection. The web app's pages and the shared ones open a
  new socket at their next request (signing in on it as before) and the apps sign in at once; through
  a bridge that only notices the relay gone at the client's next request, that request gets
  `device_unreachable` (hence the bridge's longer time). The device's own listener also takes at most
  `cMaxAnonPerAddr` (256) sockets from one address (`clientAddr`, `anonBucket`: a global IPv6
  address's /64, but the home network's own IPv6 addresses - fc00::/7, link-local, loopback and the
  device's own on-link networks, `onLinkV6` - one by one, as IPv4 ones, since every device of a home
  shares its /64) before they sign in (`anonSlots`): past that `Listen` answers 503 before the upgrade. Past `cMaxAnonDirect` (2048) in
  all, a newcomer gets in and one socket is closed for it (`pushOut`): of the address holding the
  most - the newcomer's own when it holds as many - the longest idle, one answering a request last.
  Refusing every newcomer at the total let a few addresses keeping their sockets busy lock out the
  owner's apps and pages, which open a socket before they sign in. A connection stops counting once
  signed in. Refusals and closings are counted in `anonLog`. `Listen` refuses another site's page with a 403 before
  the upgrade (`origin.go`, `originAllowed`, also the upgrader's CheckOrigin): no Origin (apps,
  otc-sync, friends' devices), an Origin whose host is the request's Host when that is a name the
  device answers to (`deviceHost`: an address, a name with no dot, one ending in `cLocalSuffixes` -
  .local, .lan, .home, .home.arpa, .internal, .fritz.box, .ts.net... -, its own name on the bridge
  (`mg.originAllowed`), a host of allowed-origins, or any the request came through a proxy for, whose
  headers no page can add to its socket: the web app at any of the device's addresses, tunnels,
  Tailscale Funnel, which passes the Host on, the owner's reverse proxy), one on X-Forwarded-Host
  from loopback or a container's trusted proxies, or one in `[otc-api] allowed-origins`
  (`OTC_ALLOWED_ORIGINS`); `null` and other schemes never. Host = Origin alone let in DNS rebinding:
  a page on attacker.example whose DNS points at the device's home address opens
  `ws://attacker.example/ws` with both headers its own; any other name of the owner's goes in
  allowed-origins. Strangers' routine
  requests log at Debug; refused origins, plain requests to `/ws`, passwords tried while locked out
  and unreadable messages before sign-in are counted, one Info line a minute (`anonlog.go`); a request
  before sign-in that panics is logged at Error with its stack once a minute and counted after that
  (`recoveredPanic`: a loop of them rotated the log away), and a friend request with no profile is refused
  before anything is dialled. `RespFriendships` never carries
  `Friendship.secret`. Push-registration syncs to the bridge go only through `mg.requestPushSync`
  (one at a time). `mediastream.Store.IssueShared(key, res)` reuses a token per resource for 5 min:
  give it a key only when it names exactly one resource every authorised caller may share (a
  gallery item, a post's media). The device still serves `ReqUploadFile` for old clients.
- `social`, `session`, `settings`, `profile`, `status` — feature-specific logic (social feed/friend
  sync, auth sessions, device settings, owner profile, RAID/disk/CPU status) sitting between
  `websocket` and `dao`. `session` also owns issue #101's in-memory session-token store
  (`session/tokens.go`): a browser can't keep the account password around the way the native apps
  keep theirs in the Keychain, so after a password `ReqAuth` it holds a single-use, TTL'd opaque
  token (`ReqIssueSessionToken` / `ReqAuthWithToken` / `ReqRevokeSessionToken`) that redeems back
  into the same already-derived `*Session`. Process-local and deliberately not persisted — exactly
  like the `*Session` objects it points at, tokens don't survive a restart (the device's own screen
  has a key that does, in /run: "A screen on the device"). At most 32 a sign-in and
  10,000 in all (oldest out), swept every minute. A password change (`ChangeKey`, whose old
  password goes through `session.Attempts` as a sign-in's does: address, 1 s delay, lockout) starts
  a new password generation (`session.PasswordChanged`): every token goes, none is issued or
  redeemed for a connection signed in before, every other signed-in connection is closed
  (`Manager.passwordChanged` over `mg.live`; friends' stay) and the changer carries on; a sign-in
  checked before the change and done after it is refused (`signIn`). `session/ratelimit.go` (issue #117) is the per-address
  password-attempt limit: 5 failures in a minute lock that address out for a minute, answered with
  `Ack.code = "too_many_attempts"` + `retry_after_seconds`. The bridge reports each relayed client's
  address to the device with `BridgeClientInfo`, so the limit applies through the bridge too.
  `ReqAuth` runs through `session.Attempts.Attempt(addr, try)`: the limit is re-checked and the
  outcome recorded while holding one of `cAuthSlots` (2, matching `deriveSlots`), so guesses queued
  behind a failure cost no Argon2 derivation. The vault is created by `dao.PersistSecretIfAbsent`
  (a conditional insert under a mutex), and `session.New` checks a lost race - two first sign-ins
  at once - against the stored vault; never add a key or migration to `vault` for this. A new
  password needs `session.MinPasswordLen` (8 bytes: `ChangeKey`, the first password `session.New`
  sets, `init-owner-password`, `ReqCreateUser.password`); one already set is never checked against it.
  Settings > Users asks for a new account's first password (web, iOS, Android): `createUser`
  checks it before provisioning anything and stores the vault in the account's database
  (`dao.OpenUserDatabase` + `session.New`) before its instance spawns, so nobody can claim the account
  by signing in first; the person changes it later with `ChangeKey`. `RespUsers.password_set` is set
  on every CreateUser answer of a device that knows the field - unset, an older device ignored the
  password and the clients say the first sign-in sets it. A user with `request_bridge_access` and no
  first password (an app from before 2026-10-23) is refused before anything is looked up
  (`dev.users.password_needed`: update the app and add them again): the account was anyone's who
  signed in first at `<username>.<bridge>`, a guessable name the bridge's public name check shows
  appearing; one for the home network only may still come without one (its first sign-in sets it, as
  `ReqCreateUser.password`'s comment says). `createUser` runs one at a time
  (`createUsers`: two tabs adding one name both passed the checks), refuses a username over 63
  characters - a DNS label, within `users.username` - before anything (`dev.users.username_too_long`),
  and anything failing once the account's database is being made (half made too), its folder or its
  row takes back what was made: the database and MySQL user (`DropUserDatabase`) and the storage
  folder, which no row would otherwise name. A too-short first
  password is answered `Ack{Ok:false, ErrorMsg}` with **no code** - the phone apps take any sign-in
  code for the bridge's "device unreachable" and would keep retrying - undelayed and outside the
  lockout count (`AuthLimiter.Attempt`); `ChangeKey` answers `code = "password_too_short"`.
  Friend requests (issue #25) can be removed by either side: `ReqDeleteFriendship` deletes the
  local row and, best effort, sends `FriendshipInterDelete` (authenticated by the shared
  per-friendship secret) so the other device drops its copy; a sender whose request was deleted
  while it was offline learns it from `FriendshipStatus.not_found` on its next friend sync.
  Issue #174: removing a friend can also `delete_their_data` (`social.purgeFriendData`: their
  posts with media, their comments - `social_publications_comments.author_domain`, the device
  a comment was synced from, never what the event claims - and likes on any post, alerts) and
  `ask_them_to_delete_mine`: `FriendshipInterDelete.forget_me` when their device answers,
  otherwise a `forget_event` in `events` with `target` = that friend (`GetEvents` serves a
  targeted event to its target only) and the friendship "leaving" (`forget_requested`) until
  their device purges and sends a plain `FriendshipInterDelete`. Everything a friend's sync or
  request deletes is checked against the sender: `mayDeletePublication` (its own posts only),
  `mayDeleteComment` (its comments, or comments on its posts), forget = the sender's data only.
  A friend connection is re-checked on every request (`FriendshipAccess`), and may read media
  of the owner's own posts only (`ownPublication`). Issue #140: a
  friend request is only stored after the receiver dials the sender's domain back and it confirms
  (`DidSendFriendshipReq`); if the receiver already has a friendship with that domain, the
  sender is that friend's re-created device, so the row takes the new secret and profile in place
  (`relinkDecision`: accepted stays accepted, pending becomes the new incoming request, blocked
  stays blocked and the request is refused) instead of failing on the primary key. A re-linked
  accepted friendship answers the sender with `Ack.code = "accepted"`, which the sender stores at
  once (`social.ErrFriendsAgain`); the sender reuses its own existing row with the new secret and
  restores it (or deletes a row it just created) when the request fails. `ReqFriendshipRequest`
  always answers with an Ack, so the clients show the reason. A friend request is one unread alert
  per domain (`dao.AddFriendRequestNotification`: one sent again while it is unread moves it up, its
  time and name) and pushes only with a new alert - a stranger re-sending one in a loop used to add an
  alert and a push each time.
  Profile pictures, the owner's and the friends' (they go out again with every Alerts row and feed
  post of their person): at most `dao.MaxProfilePictureBytes` (128 KB) is kept
  (`files_manager/profile_picture.go`, `FitProfilePicture`) - a picture within it as it came (a header
  Go reads: JPEG, PNG, GIF, WebP, HEIC), a larger one made again as a JPEG of at most 512 px a side
  within the cap, depending on whose it is: the owner's own in any format the device shows, turned as
  its metadata says (`SetProfile`, and at each start the one stored before the cap:
  `fitStoredPictures`); an accepted friend's (the sync's `refreshProfile`) or that of a device the
  owner asks to befriend only as a JPEG or PNG of at most 50 MP (`cMaxFriendPicturePixels`), by Go's
  decoders within the decode budget, once per
  picture and process (`Social.friendPicture`, keyed by the sent picture's SHA-256: the sync asks
  every pass); a stranger's friend request's never decoded - over the cap it is stored without it,
  and the sync fetches it once accepted. What isn't a picture is never kept: SetProfile then answers
  `dev.social.picture_unusable` and keeps the old one. Friends' pictures stored larger before are
  dropped at each start (`DropOversizedFriendPictures`; asked for again at the next sync) and read back
  as none (`storedPicture`; `ListNotifications` leaves them out in its query). The apps have sent a
  crop of at most 45 KB since 2026-09-29 (the setup wizard 96 KB); before, the photo as picked.
  Issue #169: every socket this device opens to someone else (a friend's device, the bridge's
  one-off calls) is a `wsframe.Client` (`wsframe.Dial`): 15 s handshake, 30 s per write/read,
  64 MiB read limit (`ReadMedia`: a whole file, 10 min, the 1000 MiB cap); friend sync closes
  each friend's socket after its pass, and Web Push uses a 15 s HTTP client. The pooled bridge
  relay socket clears the deadlines once registered. Its dialer (`wsframe/addresses.go`) looks a
  name up within 5 s (`LookupTimeout`) and remembers what it resolved to; a lookup that fails
  dials those addresses instead, each with an even share of the time left, and TLS still checks
  the certificate against the name. Kept in `.bridge-addresses.json` in the instance's working
  directory (`KeepAddressesIn`, from websocket's `Init`; at most 256 names, written temp + rename
  only when they change). Seen on Cala on 2026-10-30: an update's downloads saturated its home
  link, the router stopped answering its DNS, and with the bridge's 60 s TTL every pool dial
  failed for 20 minutes while the device ran fine at home.
  **Friend post sync** (`social.updateFriendEvents`): a file whose hash fails `dao.IsContentHash`
  is dropped (`NewSocialPublication` refuses one too, storing a post and its files in one
  transaction); the size recorded is what the files take on this disk (int64; release 92 clamped it to MaxInt32); media is pulled in
  an answer of at most the declared size + 64 KiB (`friendMediaLimit`; a JPEG 6x + 4 MiB, as a
  posted HEIC is served as its JPEG under the HEIC's size; a size of 0 or less keeps the thumbnail
  only) under `ReserveFriendMedia` of that limit (3x, waiting at most 2 min) and written by
  `writeFileAtomic`. A file's type is kept only when `mediastream.SafeType` keeps it (ServedType's
  inline list), else `application/octet-stream`, and every answer carrying a post's files maps it
  again (`safePostFiles`): the web shows them from blob: URLs in the device's origin. A comment is
  shown with the friendship's name for its `author_domain` (the domain without one), the owner's
  name for theirs, `publisher_name` only for rows from before release 51 (`dao.queryComments`), and
  a friend's is stored with the name the friend is known by, never the one its event signs. A transport failure (`errFriendTransport`, including the bridge answering
  `device_unreachable`/`account_disabled` for the friend) or a failed thumbnail write stops the page
  at that post without moving `latest_sync` past it; after 3 stops in a row (`cPostTries`) the post
  is given up. `GetEvents` never serves the last second and completes a full page with the rest of
  its last second; same-second rows come in `INDEX(dt)` order (insertion order) - don't add a
  tie-break. A friend's feed page is capped at `dao.MaxFriendFeedPage` (20). **Likes**: action
  `delete` removes the sending domain's like; any other action, `""` included, is a like. Likes
  insert with NOT EXISTS and only a new row notifies; release 92 added unique keys `like_once
  (pub_uuid, friend_domain)` and `comment_like_once (comment_uuid, friend_domain)`.
- The bridge shared secret never reaches a client (release 66): `GetSettings` leaves it out and
  `SetBridgeSecret`/`RegenerateBridgeSecret` are refused - the device pairs and rotates it itself
  (`regenerateBridgeSecret`), and no Settings screen shows or edits it. Its name on the bridge too:
  `SetSettings` (no app has sent it since 2026-09-20) is answered done for the name the device has
  and refused otherwise (`dev.device.domain_managed`) - it rewrote the name unchecked, which made the
  device leave the bridge 15 minutes later or fail every registration, with nothing to set it back.
- `push` — Web Push (per-device VAPID keys) and iOS pushes. The APNs auth key is the developer
  team's private key and lives **only on the bridge**: a device never has an `[apns]` section, it
  relays title/body to the bridge (`BridgeNotify`), which sends to the tokens that device itself
  registered - so a device can only ever reach its own phones. The device runs
  `Push.StartAsync(256)` (one ordered worker, inline when the queue is full); the bridge stays
  synchronous and builds its APNs/FCM senders once, so a new .p8 or service-account JSON needs a
  restart.
- `api` — the small HTTP layer: healthcheck, the `/ws` upgrade, and static file serving (serves
  `web/dist` copied to the device's static path; appends `.html` to extensionless paths for
  client-side routing). Pages (`.html`: index.html, the SPA fallback, a route's page) are
  `Cache-Control: no-cache` with a content ETag (`pageETag`: a release unpacks its bundle with
  its build times, so Last-Modified alone could keep the old page); Vite's hashed
  `/assets/<name>-<8 chars>.<ext>` are `immutable` (`staticCacheControl`, the bridge's
  `cHashedAssetPath` rule; `TestServeStaticCacheHeaders`). The bridge's relay of these files sets
  no Cache-Control, Last-Modified or ETag on HTML, so browsers never keep a page from it.
  Listener bind errors are logged, not fatal.
- `log` — leveled logger with size-based rotation, configured once in `main()` from `[logger]`.
  A log file that can't be opened or read (a read-only card, its folder gone) no longer ends the
  process - which used to hang holding the logger's mutex, then exit into a restart loop: the lines
  go to stderr (journald) and the file is tried again every minute (`toStderr`, `retryLogFile`).
- **Plaintext never on the SD card** (issue #156, releases 62/65; devices build `go build ./bin/otc.go` - one file - so `bin/` must stay a single file and helpers live in packages like `hardening`): the service `mlockall`s its memory at
  start (`hardening/hardening_linux.go`; the unit has `LimitMEMLOCK=infinity` and `LimitCORE=0`; `[otc]
  disable-mlock=true` turns it off), swap is zram only (`/etc/rpi/swap.conf.d/90-otc-ram-only.conf`,
  no `/var/swap` writeback; dphys-swapfile removed; Makefile.pi's `swap` is no longer in bootstrap),
  and `TMPDIR` is a per-process `otc-<pid>` directory on a tmpfs (`/tmp` when it is one, else
  `/dev/shm`), made by the process and never taken as found (`ownTempDir`: `otc-<pid>-<random>` when
  the name is taken; 0700, ours), with dead processes' directories swept at start (a supervised child resolves an
  inherited `otc-<pid>` TMPDIR to its base, `tempBaseCandidate`, so it never sweeps the primary's).
  At `level=info` the log never names
  a file path, search term, share path or Wi-Fi network - those are Debug only - errors name hashes,
  and no request is dumped whole (a friendship secret once was).

### Localization (`docs/i18n.md`, `i18n/`)

Every surface ships in English plus es, fr, de, it, pt (pt-PT) and nl since 2026-10-11 (device
release 119, otc-sync 0.16.0, macOS 2.2, the bridge's site, account page and emails; the iOS and
Android builds follow once their store reviews clear). Translations were made and reviewed by
back-translation (`"reviewed": "claude-backtranslation …"`); a new or changed English text needs
all six before `I18N_RELEASE=1` passes. The design is `docs/i18n.md`; the contributor guide is
`i18n/README.md`. The user's language is
stored on the device per user (`Settings.language` / `Status.language`, `optional`: absent =
device predates it, `""` = Automatic, `SetLanguage` = 138), and every app sends its effective
language as `ReqEnvelope.lang` (= 2); a request without it gets today's English, byte for byte.
One source, `i18n/strings/<code>/<prefix>.json`, generates every platform's files: `make i18n`
(web `web/src/i18n/`, Apple `OffTheCloud/i18n/` with the hand-written `L10n.swift`, Android
`cloud/offthe/otc/i18n/` with the hand-written `UiText.kt`, Go `i18n/catalog` + `i18n/*_gen.go`
for package `i18n`'s runtime, the wizard's `# BEGIN GENERATED I18N` block); never edit those
generated files or the catalogs in Xcode. `make i18n-check` validates (placeholders, tags,
glossary in `i18n/glossary/`, staleness), fails on a stale generated file, runs the
hard-coded-text ratchet (`i18n/scan/README.md`: a file whose count of literal UI text rises
fails - lower the baseline with `go run ./i18n/cmd/i18nscan -update -path <files>` in the commit
that moves text into the catalog) and builds what embeds the catalog. It gates
`scripts/release.sh` (on a `git archive` of HEAD, with the web app's packages installed there by
`npm ci`; needs `make pb`'s `proto/generated`), `scripts/desktop-release.sh` (on a `git archive`
of HEAD, its Go protobuf code generated there; its web scanner reads `web/node_modules`), `make -C
bridge bridge`, `make android-release` and CI (`.github/workflows/i18n.yml`). Codes before prose: clients check `error_code` first and read
the English message only when the code is empty (`duplicated_file` on UploadFile/FinishUpload/
LinkFile, from `filesmanager.ErrDuplicatedFile`, and on BeginUpload for `SpacesNameError`; `unknown_payload`) - iOS/macOS `ErrorCodes.swift`,
Android `net/ErrorCodes.kt`, otc-sync `engine/requests.go`. Machine alerts (update, RAID, disk
space, Wi-Fi wiring) live in `websocket/machine_alerts.go`; `raidwatch.MissingPorts()` keeps the failed disks'
ports as data; update alerts are one per `notifications.update_version`, with the legacy English
titles still matched (`updateTitles` keeps them even if the title is reworded).

Moving text into the catalog (phase 2; docs/i18n.md "Text the device writes" and "Each
platform"): a request without `lang`, with `en`, or in a language the build doesn't carry renders
today's English byte for byte (`devtext.English`), and tests that compare English keep their
expected strings. Device replies: packages return devtext errors (`devtext.New`/`Wrap`/
`WithEnglish`), reply sites use `devtext.Text(lang, err)` or `websocket/replies.go`'s `errText`/
`sayOr`, and a raw error the device can't translate goes after `dev.err.action_failed` in another
language. Alerts: `alerttext` (dao paragraph above). The apps show the device's and the bridge's
words as they come (already in the request's language) and keep their own text as keys or a
`UiText` until shown: web `components/uiText.ts` (`say`, `asIs`; `percentText`/`unitText`/
`plainNumber` keep hand-written numbers byte-identical), Android `UiText` plus
`ui/common/AppText.kt` (`now()`, `TextIOException`) and `.english()` in JVM tests
(`test/.../i18n/English.kt`), Swift computed accessors and `LocalizedError`s, otc-sync `Msg()`
errors. iOS model text is an enum worded when shown (`GalleryNotice`, `PeopleNote`, `FriendsToast`,
`ComposerAlert`), Settings' `SettingsText` (`say`/`asIs`/`error`) or a private `@Published` `() ->
String` behind a computed `String` (`FilesExplorerViewModel`); Android picks the device's words or
ours with `deviceOr`/`Throwable.shownText()` (`ui/settings/SettingsViewModels.kt`) and puts a key's
text inside another key as a nested `UiText.Res(id, listOf(nested))` (`PhotoMonths.kt`). Shared
patterns (docs/i18n.md "Each platform"): a date written by hand is a key with `text` pieces
("{month} {year}", "{day} {month} {year}"; month names from `Intl.DateTimeFormat(t.tag, {month})` on
the web, `app.photos.months.*` on the phones); a sentence that would need two plurals takes its
counted pieces, each its own plural key, as a `{what}` text argument; name lists are two recursive
keys ("{first} and {second}", "{first}, {rest}"), never `Intl.ListFormat`; a number written by
hand follows the language's canonical tag, never the region (web `plainNumber`/`percentText`, iOS
and Android `decimalText`, the Mac's and otc-sync's `desk.main.percent`), so English stays byte for byte; a typed confirmation
sends the code constant (the account page shows and accepts `site.account.delete.word`, and
`delete`, but always sends `{"confirm": "delete"}`; deleting a user in Settings asks for the
username). The setup wizard (`wiz.*`) takes `?lang=` from the apps (iOS `BLESetupLang`, Android
`withLang`, Mac `BLESetupTransport.withLanguage`); in a browser its page has a language list
(Automatic first, which drops `?lang=`), hidden inside the apps, which call
`window.otcSetupLang(code)`: it reloads with `?lang=` on the same step, keeping the state in
sessionStorage (never passwords or the terms checkbox); the Mac's setup window has its own
language menu (`LanguageMenu` in `LanguagePicker.swift`). It words install.sh's steps from its
`[otc-install] @step|@error <token>` marks, only when the key's English equals the line that
follows. The bridge's landing, comparison, account, not-found, disabled and unavailable pages, and
the Google/Apple sign-in's own two (`signin.html`, `continue.html`, `site.signin.*`), are templates in
`bridge/site` (Frontend below); its emails follow `accounts.lang` (their links go to
`/<code>/account` for a carried non-English language; `mail.date.long`'s `{day}` is a count only so
French can write "1er janvier"), while its JSON `error` texts and websocket replies stay English (a
fallback only) and clients word them by `code`.

The stored choice (`docs/i18n.md`, "The stored choice"; every client follows the same rules):
- Device (release 118, `scripts/updates/118.sh`): `settings.language` and `last_ui_language`.
  SetLanguage is owner-only (`websocket/language.go`), compare-and-set under `settings.langMu`,
  Ack codes `changed` and `invalid_language`. `processMessage` passes `requestLang(env)` to the
  three `process*` handlers as `lang`, never as connection state (requests run concurrently).
  Owner push registrations write `last_ui_language` after they succeed. The push language is
  `push.ResolveLanguage` -> `UpdatePushRegistrations.language` and `Push.Lang()`. A binary newer
  than its schema gets `dao.ErrSchemaBehind` (MySQL 1054) and carries on with the defaults.
- Bridge: migration `010-language.sql` (`accounts.lang`, `push_registrations.language`), tolerated
  when absent (`bridge/dao/language.go`); `PUT /api/account/language {"lang"}`; sign-up takes the
  body's `lang` else `Accept-Language` (`""` when none matches); `/me` carries `lang`; the account
  page sends its own language at sign-up from a non-English page and, on a non-English page,
  PUTs it once for an account whose `lang` is `""` (never overwriting one, never sending `en`);
  the export adds `account.language` and `domains[].push_language`; the offline alert's language
  comes from `push_registrations.language` (`offlineAlertText`). Never log a language next to an
  account or domain.
- Apps: a local copy plus a pending change, sent with SetLanguage against the device value seen
  when it was made. Sign-out, Log Out, Disconnect or another device/password keeps the copy and
  drops the pending change - except one made for the next device (no device set up; on macOS,
  otc-sync and the phones also none of its values seen yet, e.g. a wrong password - the phones
  keep `seen` in memory only, forgotten when the device turns the password down and at
  `OTCConnection.invalidate()`; picked in the Mac's setup wizard; or still pending when a phone
  leaves a device from the "isn't available"/"can't connect" card, `loggedOut(carryPending:)`;
  macOS `languageForNextDevice`, otc-sync `language_next_device`, iOS/Android `forNextDevice`),
  which the next sign-in sends without `expected`. The web and the phones send nothing before a
  sign-in is accepted (phones: `Device.signedIn` / `LanguageDevice.signedIn()`) and send at every
  accepted one, also on an already-open socket, ahead of its first Status/Settings (phones:
  `LanguageSettings.connected()` from `OTCConnection`). On the phones new credentials alone only
  forget `seen` (a change made signed in keeps its `expected`; "changed" adopts the device's
  value), Settings' Log Out drops a change made signed in, and a relaunch keeps a pending one.
  The phones' `LanguageMenu` (a globe and the language's own name, Settings' list) is on Welcome,
  the Bluetooth setup and the two cards; the setup page is asked with `window.otcSetupLang`, and an
  older wizard is reloaded once installing (`ios.`/`android.setup_language_later` until then).
  The 30 s window runs from the change or its last send.
  `unknown_payload` keeps it pending until the next connection. `lang` is set only in
  iOS/Android `WSClient.exchange`, macOS `WSClient.envelope`, otc-sync `Client.Request` and web
  `ws.ts`. The Language row/panel/picker/tray submenu is hidden while
  English is the only shipping language. Parity: iOS `LanguageSettings.swift` <-> Android
  `i18n/LanguageSettings.kt` (rules in `LanguageSync`); macOS `LanguageSettings.swift` <-> otc-sync
  `engine/language.go` + `internal/oslang`; `otc-sync language [auto|<code>]` (English output).
  Android recreates the Activity on a change, except on the Bluetooth setup (the manifest handles
  `locale|layoutDirection|uiMode|density|fontScale`, and `MainActivity.onConfigurationChanged`
  recreates for a new locale, dark mode, display or text size unless `LanguageSettings.holdInPlace()`
  is held, so the setup's link and page survive a language pick and a dark-mode or text-size
  switch alike - Compose follows in place; Onboarding's `setupOpen` is saveable, so a recreation
  that does happen comes back to the setup): state that must survive uses `rememberSaveable` or a
  view model (the connection forms' `ConnectionFormModel`; never a password in saved state), and
  launch-time prompts ask only on a fresh create. `node web/scripts/i18n-test.mjs` tests the web runtime and choice.
- New web text goes through `useT()` / `<Trans>` from `web/src/i18n` with an entry in
  `i18n/strings/en/web.<area>.json`; never keep translated text at module level or in state
  (keep a key or a `UiText`). A `UiText` is a function, so never hand one bare to React:
  `useState(() => say(…))`, `setX(() => text)`. Round a value that isn't whole with `toFixed`
  before `unitText`/`percentText` (Intl rounds ties differently), write B/KB/MB/GB sizes with
  `components/sizeText.ts`, and format dates with `fmtDate(t.lang, d, options)` (`dateTimeFields`
  for `toLocaleString()`'s fields), never `toLocaleString()`: the page's language, in the
  browser's own format when it is that language. The public `/shared` page shows the visitor's browser language (Automatic,
  or that browser's own stored choice).

### Bridge (`bridge/`)

A separate deployable with its own `dao`/`websocket`/`api`/`makefile`, sharing only `proto/generated`
and `cfg` with the device module. It maintains a pool of authenticated device WebSocket connections
keyed by domain (`bridgePool` in `bridge/websocket/websocket.go`) and proxies friend/browser traffic
to the right device — the device never accepts inbound connections directly.

Relay invariants: a `/ws` socket not yet paired has a 2 min read deadline (`cUnpairedReadTimeout`)
and is closed on every return path unless a device relay owns it (`handedOff`); one address has at
most 64 of them open (an IPv6 /64, and 256 per /48: `[bridge] max-unpaired-per-addr`), and their messages are
read with 64 KiB outside a budget of their own (`unpairedBudget`, 256 MiB), held until each is
handled - a pairing's first request until the device has it. One address (an IPv6 /64) may hold 16
MiB of it and one /48 32 MiB (`cUnpairedShare`, `cUnpairedWideShare`: `wsframe.NewShare`, a budget
within another), so one sender's unfinished messages can't make everyone else's - a device's logs,
a friend request with a picture - wait 30 s (`wsframe.RoomWait`) and fail. A paired client's messages are read
with 256 KiB outside `frameBudget` (`frameRules`): 32 in flight held 4 MiB each outside it. An
unpaired socket is closed after 30 requests answered "unreachable" or refused
(`cUnpairedMaxRefusals`; the web's unreachable screen asks every 5 s and reconnects). A device's
disabled flag is read through `Manager.DeviceDisabled` (`hosts.go`): remembered 5 s
(`cDisabledTTL`; a change through this node shows at once), and looked up only for a host that can
be a device's (`IsDeviceHost`: one label under `[otc-api] tld`; an IP, a port or a name of another
domain never) at most 5 a second per address after a burst of 30. A device's message (registration,
secret rotation, release, logs, name check, user address, disabled state, push) whose identity can't
be a device's - an owner, domain or secret past its `devices` column (64/150/150 bytes), a domain of
other than host-name characters - is refused as a wrong secret before anything in it is looked up
or logged (`identity.go`); its info lines name the domain only once the secret checked out, and
refusals are a line a minute per address (`logRefusal`), as is the API's cross-origin refusal, with
the Origin and path cut to 160 bytes. Every write to a
client goes through `writeClient` (fragments above 256 KiB, a fresh 60 s deadline per piece, so only
a client that stopped reading trips it) - never on a device socket; `pingLoop` uses `WriteControl`
so pongs flow during large uploads. A forward waits for its reply 90 s (`cForwardTimeout`) from the
last sign of the device working on it, not from when it was sent: on a connection it opened to the
bridge, a device names the requests running 10 s or more every 20 s in an unsolicited pong
(`otcbusy` and the ids as uvarints; `websocket/bridge_busy.go`, read by `busyIDs` in `busy.go`), so
a post whose videos are cut, a big folder deleted or a large file over a slow line is waited for -
up to 2 h (`cForwardMaxWait`) - while a request the device stopped answering, or never names, still
fails at 90 s. The reply of a request sent earlier on the same connection also starts the clock
again for the ones sent after it, which waited behind it (a device sends its replies one after
another: a video's span asked ahead, on a slow line); a later request's reply never does, so a
forgotten request still fails. A one-off (a static asset, a `/media` span) is waited for at most 3 x
45 s in all however long it is named (`oneOffMaxWait`, what its 3 attempts took before), since it
holds one of the device's 3 one-off slots meanwhile, and a `/media` response's session is closed as
soon as its client hangs up (`Session.Close` from `context.AfterFunc`), its first span and probes
included. A bridge from before this takes those pongs for any pong, and a device from before it
sends none and is waited for as it always was. A client's request the device keeps naming is ended
by the client's own clock, not the bridge's 90 s: for a page or a sign-in, the apps' 120 s
(`PAGE_TIMEOUT_MS`/`pageTimeout`, `SIGN_IN_TIMEOUT_MS`/`signInTimeout`), after which the screen asks
again with more patience. Device replies are read under `replyBudget` (8 GiB), and
`forward()`'s `release` is never nil. Pairing is limited per client address (IPv6 by /64) and Host
to 64 open (`[bridge] max-paired-per-addr`), 2/s with a burst of 30, and per device to 64 in use
(`[bridge] max-paired-per-device`). A paired session is a stranger's until the device accepts a
sign-in on it (`sessions.go`; read off the relayed frames, so for devices of any release: a payload
of Auth, AuthWithToken or AuthAsFriend answered by an Ack with ok and no error - `isSignIn`,
`isAccepted` - and never for an id sent twice at once). Strangers' sessions count against a share
per address and device - an IPv4 address, an IPv6 /64 and its /48, a quarter of the device's cap
(`[bridge] max-unauthenticated-per-addr`; a newcomer waits up to 3 s for room, as a household's
tabs sign in) - one with nothing in flight is closed after 2 min without a message
(`cAnonPairedIdle`; the web app and the shared pages reconnect with their next request), and at the
device's cap a newcomer closes one of the address (or IPv6 /48) holding the most: the one paired the
longest ago, 5 s or more with nothing in flight (`cEvictAfter`) or 60 s or more whatever it is doing
(`cEvictBusyAfter`; its requests are answered "unreachable") - by age, not idleness, as strangers
sending a small request every few seconds were never idle (`evictAnon`). Signed-in sessions are never closed for anyone. A client whose relay dies while
it has nothing in flight is hung up on at once (`deviceRelay.dead`), so its next request pairs anew
instead of being told the device is unreachable. A relay records its owner uuid (never the secret), and a
registration evicts other owners' relays. `DropDomains` (release, account deletion,
`BridgeReleaseDomain`, inactivity) and `DropReplacedIdentity` (claim replace, `accountNewIdentity`)
publish on Redis `otc:drop` for every node, and every 20 s each node checks its domains against
MySQL (`dao.DeviceOwners`). One node sends the offline alert per outage (`otc:alert:<domain>`),
naming the device (`push.offline.body_named`, `{name}` = the domain's first label) only when its
account holds several domains (`dao.DomainsInAccountOf`). The bridge's Web Push client reaches only
public addresses, without redirects.

Fleet tab (`bridge/fleet`): every host's `otc-fleet-agent` (unprivileged user `otc-fleet`, passwords
as systemd credentials) and every bridge node (`fleet.Reporter`, counts from
`Manager.FleetStats`) write a JSON snapshot to Redis every 30 s (`fleet:host:<name>`,
`fleet:bridge:<node>`, 10-minute TTL, `fleet:known-*` remember the last report);
`GET /admin/api/fleet` reads them (`fleet.Read`) and judges each host with `fleet.Evaluate` - the
one function the email alerts use too (`fleet/alerts.go`: one node at a time under
`fleet:alert-lock`, bad twice in a row or warn 5 times before mailing, reminders every 6 h, one
"resolved", grouped per minute, state in `fleet:alert-state`; `[fleet] alert-to`). Snapshots carry
counts and host figures only - never a domain, account or address; MySQL errors lose their quoted
values.

Growth tab (`GET /admin/api/growth?days=N`, `dao/growth.go`; migration 011): the site's anonymous
visit counts and the GitHub download counts, with the device figures (names; active = a client
relayed in the last 1/7/30 days, `last_client_at` - never `last_online_at`, which nearly every
device that is switched on has). `serveSite` counts a page someone opened
(`api/visits.go`: own host, GET, 200, no prefetch, no robot by User-Agent, which is never kept)
under day, page and source - `ref:<tag>` from `?ref=`/`?utm_source=`, else the Referer's host
(never a device's address or an IP; nothing but `ref:` for `/account`) - added up in memory and
written with the traffic counts (`flushPass`), at most 1,000 named sources per day per node, the
rest under `(other)`. `bridge/downloads` polls the releases every 6 h into `download_counts`
(`src.tar.gz@v<N>`: devices that took release N). Both pruned after 25 months. Download clicks by
source (`site/downloads.go`, `api/clicks.go`): the pages' download buttons and store links go to
`/get/<target>` - a fixed list (`pi-image`, `windows`, `windows-arm`, `linux`, `linux-arm`, `mac`,
`ios`, `android`; anything else is the 404), a 302 to that target's file or store page, never
anywhere else - which counts a person's click (`personVisit`) into `site_visits` under the page
`/get/<target>` and the source (no schema change; the visit figures leave `/get/%` out, and the
tab's "Downloads by source" reads only it). The source is the click's own `visitSource`, else the
`?s=` (`site.SourceParam`, which visits never read) that the pages write into their links to the
site's pages and downloads (`site.Options.Source` = `linkSource`: the request's `visitSource`, else a
carried `s` that `carriedSource` finds valid; none on a device's address or for a robot; the account
page's own Referer never), so it survives the hop from a comparison page to `/#install`. Templates
use `{{.Download "<target>"}}`, `.Signup` for `.Account`'s sign-up, and `.Carry` around a rich text
whose tags link the site (`Carries` in `tags`). Google Play's link gets `&referrer=` with
`utm_source=<name>&utm_medium=off-the.cloud`, the App Store's `pt`/`ct` (40 characters)/`mt=8` only
once `appStoreProvider` is set (then name the App Store in the privacy page too). The privacy page's
"Visit counts" row describes all of this: keep the two in step, and the record in
`private/gdpr/records-of-processing.md`.

### Frontend (`web/`)

React 19 + TypeScript + Vite, with no router (one address; App's `tab` is the section). The
signed-in shell is Google Photos-like (App.tsx, index.css). A fixed top bar holds the menu
button, the logo and the search (`TopSearch.tsx`: chips
for the open collection, people and tags inside the field; no panel until something is typed, then the
matching things (5 tags), named people (the 5 with the most photos) and files and folders by path
(`SearchFiles`, debounced, stale answers dropped), plus "Search documents for x" - every match (`SearchFiles`
limit 50) listed in Files over the folder, which stays as it was; neither on a device answering `unknown_payload`.
A picked folder or file opens in Files through `filesNav.ts` - a file as a click there would), and at the right
the account button (`AccountMenu.tsx`: picture, name and device address, Settings, Sign out through
`net/signOut.ts`). Signed out, the bar holds a password field beside Sign In instead (`TopSignIn.tsx`, which
calls `net/signIn.ts`; on a phone Sign In opens the field across the bar); `views/SignIn.tsx` is only a new
device's setup, shown when GetPubKey says `is_new_device`. The left menu
(`Sidebar.tsx`) lists Images, People, Collections, Places, Files; Sharing: Social, Friends; Device: Alerts, Settings;
storage at the foot. People - menu item, page, search, person filter - exists only while face recognition is
on (`faceRecognition.ts`: asked after sign-in, set by Settings' switch, the last answer kept in
`otc_face_recognition` so a reload doesn't flash it; unknown means hidden). Three widths: ≥1024px it is
whole or a rail of icons with labels (menu button, remembered in `otc_menu_open`); 600-1023px always the
rail, the button lays the whole menu over the page; below that only the overlay. The window scrolls -
nothing may give `main` (or another ancestor of the pages) an overflow, or `position: sticky` measures from
it (release 103's 64px gap) - so sticky and fixed things offset by `--topbar-h`, and a page's own fixed bars
use `left: var(--content-left)`.
**Places on the web** (2026-10-29, the phones' rules: iOS paragraph under the apps): the menu item after
Collections at every width (the menu is always on the left), only while Places is on - the user's switch
in Settings (`Settings.places_enabled`; a device before Places doesn't say, and has none) - as People
while face recognition is on (`places.ts`: `GetSettings` asked at each sign-in, `otc_places` as
`otc_face_recognition`; Settings' section after Face Recognition, "Enable/Disable Places" with the
`web.settings.places.hint` consent text, shown only when the device's Settings carry the field, sets it
at once; a map answer `places_off` - turned off on another app - sets it off; off drops an open area;
a new sign-in or a sign-out forgets every place, thumbnail and the map's last view - another user's
library). While the device still reads where the photos were taken (`PhotoPlaces.reading`, just
turned on) the head says "Finding where your photos were taken…", answers are kept 15 s
(`cKeepReadingMs`) and the screen's tiles and the count are asked again on that beat; the empty note
waits until it is done. `web/scripts/places-switch-test.mjs` tests the gating. The page
(`PlacesView.tsx`, a lazy part with MapLibre GL JS, pinned in package.json; its worker is Vite's copy of
`maplibre-gl-worker.mjs` from 'self') shows what Images would (`photoFilter`'s tags, people and collection;
the top bar's picks stay on the map), on OpenFreeMap's dark style recoloured to the app's greys, names in
the app's language (`name:<lang>`), towns' dots, one-way arrows and road numbers dropped. Everything the map
fetches (style, TileJSON, tiles, fonts, sprite) goes through `mapFetch.ts`: a worker made from a data: URL,
whose opaque origin sends `Origin: null` and no Referer, so OpenFreeMap never learns the device's address
(MapLibre's `otcmap://` protocol; index.html allows `connect-src https://tiles.openfreemap.org` and
`worker-src data:`, nothing else). A browser that can't start that worker gets no map ("The map couldn't
load", with Try again), never a fetch from the page, which would send `Origin: https://<device>...`
(`node web/scripts/places-test.mjs` checks nothing is fetched without it). Zoom buttons are ours and
the credits are written out (compact, MapLibre
draws them from data: images img-src refuses). The arithmetic is `placesMath.ts` (`node
web/scripts/places-test.mjs`): device zoom = floor(MapLibre zoom + 1) (512 px tiles there), the screen's
tiles and one around, centre first, 64 a request, camera at rest 200 ms; the next zoom's 600 ms later;
`placesData.ts` keeps answers per (filter, zoom, x, y) for 5 minutes or until `dropPlaces()` (Images' deletes
and reloads), draws what it has at once and keeps the last zoom's pins until the new zoom's first answer.
Pins closer than 54 px merge (`mergePins`: biggest first, newest cover, the pin at the member nearest the
weighted mean, so it stands on a photo; across the antimeridian the short way, the area then west > east);
a pin is a MapLibre `Marker` around a button (the small thumbnail by path, 24 a `GetThumbnails`, on-screen
first; ember count; label `web.images.places.pin` or the photo's date). One photo opens in the viewer; a group
flies to fit its area, at least twice as close, while the zoom is under 16 and it spans over 25 m, else opens Images with
`photoFilter.showArea` - `SearchPhotos.area` on every page, the month counts and the upward pages, a "Map area"
chip (× `leaveArea`), the menu marking Places - and Back finds the map where it was (`keepCamera`). While the area is on, Images starts with a
"Back to the map" button (`.pg-tomap`, an arrow and `web.images.places.back_to_map`, above any person
or collection header, at every width; owner, 2026-10-30: the menu's Places wasn't obvious): it does
what the menu's Places does - `leaveArea`, the map where it was - and stands in for the place's
history entry (App's `onShowMap` sets the tab, as Images back to Collections does), so Back from the
map goes past it; the Map area chip stays. Opening
the page clears the area; the map is never wider than one world (min zoom from its width). It opens framed
on `located_area` (max zoom 11) - photos all over the world: the world, centred on zoom 0's biggest place -
or the world with "No photos with a location yet".
**On this day on the web** (2026-10-30; `memories.ts`, `MemoriesStrip.tsx` + `.css`, `MemoryViewer.tsx` +
`.css`, a lazy part; the phones' rules, Android paragraph under the apps; texts `web.images.memories.*` and
`web.settings.memories.*`, the phones' English and translations where they say the same). `memories.ts`
(module state; App's `watchMemories` forgets it all at each sign-in and sign-out and asks at once) asks
`GetMemories` for day, week and month - the browser's date, IANA zone and offset - one question per period
and generation at a time (`askPeriod`: a second caller waits for the answer on its way; an answer for an
older generation is dropped - a strip mounted before App's reset once lost its day answer for good); again at
the next midnight or a tab shown on a new day, every 2 minutes while an answer says `measuring`, after a hide
and after Settings' switch. The period shown (`choose`): the one picked this session while it has memories,
else the first of day, week and month with any, decided only once the earlier ones answered; a period that
answered none is a disabled chip; none anywhere, `memories_off` or `unknown_payload` = no strip. The strip
(PhotoGallery, under the person header) shows only over the whole library from its top: no tag, person,
collection or map area (`isFiltered`) and no scrubber jump (`cutoff`); it keeps out from under the scrubber's
column as the grid does (its Month chip sat under the years), and arriving above a grid already scrolled it
scrolls the page by its height. Cards: the cover's small thumbnail by path (`fetchThumbs`, 24 a
`GetThumbnails`, `thumbFor`), "N years ago", the place; the row scrolls sideways on its own. A card opens
`MemoryViewer` (fixed, z 55, black): 4 s a photo as the progress segment's CSS animation
(`animationend` steps, `animation-play-state` pauses: the button, a press held 250 ms, the menu, See all,
the viewer over it, a picture not yet drawn - 8 s at most), a tap on the left third back, elsewhere on, the
side buttons with a mouse, the next memory after the last photo and closed after the last memory; keys on the document while it is open (←
→, Space, Esc - the focus may be on the Undo note), Tab kept inside; opened from the keyboard (`detail` 0)
it starts paused, the web's stand-in for the phones' screen-reader rule. The line: "N years ago · 30 Oct
2023 · place" (`fmtDate`/`fmtDateRange`, a new formatter: `Intl.DateTimeFormat.formatRange`), "With …"
for named people; the big thumbnail for the photo and the next two over a blurred small one. The menu
(`web.images.memories.options`) hides the photo (it leaves the slideshow, which stays open), the memory,
the day of the photo shown or a named person (only a name exactly one person in People has: Memory carries
names, HideMemory an id) - the last three close it; `refuseOnDemo` first; then `refreshMemories` (old answers
on screen until the new come) and the strip's note "Hidden from your memories" with Undo (`HideMemory`
`show`); a refusal says the device's words or `hide_failed`. See all: a grid of the memory's photos over the
slideshow, a tile opens `MediaViewer` (no Delete there). Settings' Memories section after Places (only when
the device's Settings carry `memories_enabled`): Enable/Disable On this day (`SetMemoriesEnabled`,
`setMemoriesOn`) and "Show hidden memories again (N)" while today's day answer's `hidden_count` is above 0
(`HideMemory show_all`). Checked end to end in muted headless Chrome against a Docker test device built
from main with demo photos dated on 30 October of earlier years.
**Smart search on the web** (beta, 2026-10-30; `smartSearch.ts`, `photoFilter.ts`'s `text`, `TopSearch.tsx`,
`PhotoGallery.tsx`, Settings; Android's rules; texts `web.shell.search.smart_for`, `web.images.smart_search.*`,
`web.settings.smart_search.*`, the phones' English and translations where they say the same). Whether it is on
is `smartSearch.ts` (`GetSettings`' `smart_search_enabled` at each sign-in, `otc_smart_search` as
`otc_places`; absent = a device before it = off: no Settings section, no panel row, `text` never sent). Beside it
the model's state (`Settings.smart_search_model`, release 152: the device downloads the model only once Smart search
is first turned on; absent = an older device = ready; on by default from 152): while on and not ready there is no
panel row. Settings asks `GetSettings` again every 3 s while it shows the download (`followSmartSearch`: until ready,
off or left); elsewhere it is the last answer's (sign-in, Settings, a refused search), and the search field asks once
as it gets the focus while on and not ready (`smartSearchFocused`, at most every 5 s), as on the phones. An answer to
a question asked before a switch change is dropped. `node web/scripts/smart-search-test.mjs`. While
on and ready, the search panel's first row is "Search photos for “x”" (sparkle), and Enter takes it - unless the words
are exactly a tag or a named person's name, which Enter takes, the row then following things and people
(a partial match never wins: "beac" searches by description, "beach" is the tag); the documents row stays
after it. Picked, the words are `photoFilter.text` (`searchText`; one at a time, a new one replaces it),
shown as a chip of its own after the tags (sparkle, ×, Backspace takes it first); the clear button and
Images in the menu remove it too. Images sends it as `SearchPhotos.text` with the chips' filters: ranked as
the device answers, `isDateOrdered` false - no month titles, buckets, scrubber or upward pages - and no
person header or On this day strip (`isFiltered`). `smart_search_off`, `smart_search_unavailable` or
`smart_search_downloading` (another app turned it on a moment ago): the device's words in Images' note (else
`web.images.smart_search.failed`) and the text dropped, so the chips are searched by date again;
`smart_search_off` also turns it off in the app, `smart_search_downloading` keeps it on and asks the model's state. `smart_search_reading` on a page
shows "Your device is still getting to know your photos…" above the results (`role="status"`), gone on the
first page without it; nothing is asked again for it. Settings' section after Memories: the heading with a
Beta badge (`.sf-beta`), the footer text, Enable (at once) / Disable (an in-page `ConfirmDialog` first: what
the device learned is deleted); refusals show the device's words; after either the model's state is asked. Under
the hint, `download_note` only while the switch is off and the model missing (on and missing: nothing until the
device says it is downloading - no text assumes the user turned it on); while it downloads, a
`role="progressbar"` bar and `downloading` with its percent, or the device's `problem` in its place (it retries by
itself); a hidden `role="status"` says only the start, a problem and the end (`announcement`), never each percent,
emptied after 5 s. **Background work** (`settings/BackgroundWorkPanel.tsx`, rules in `settings/backgroundWork.ts`,
tested in smart-search-test.mjs; texts `web.settings.background.*`): `Status.background_tasks` (release 152: new
uploads' analysis, thumbnails, places, On this day's measures, Smart search's descriptions, photo dates...), a
section right after Storage only while the list isn't empty - per task the device's `text`, `role="progressbar"`
(done of total, labelled by the text; indeterminate, no value, while total is 0) and `count` - with no timer of its
own: it reads `useDeviceStatus`' latest Status (`useLatestStatus`), which Settings' own 5 s poll (MirrorDiskPanel's)
and the menu's keep fresh, as the phones use their Settings' Status poll.
The download states and Background work were checked in muted headless Chrome against a fake socket. Checked end to end in muted headless Chrome
against a Docker test device built from main with the SigLIP files copied in and the demo library: English
and Spanish descriptions, a description within a tag chip, the reading note, off with its confirmation.
Image groups are called **collections** in every UI (web, iOS, Android, site, store texts); the protocol
and code keep `ImageGroup`/`groupId`. Inside an open collection the selection bar has **Remove from
collection** (`RemoveFromImageGroup`, release 129: the membership of each selected photo's hash, never the
file). The web app always has a device that knows it (the device serves it); the phones may not, and turn
its `unknown_payload` into "needs an update" with an Update button for the primary's owner (`ReqApplyUpdate`)
or, for a user, who to ask. **Favourites** (owner, 2026-10-31) is a collection every device has, listed
first, that can't be renamed or deleted and fills from a heart in the viewer (empty, or full while the photo
is in it). It is the `image_groups` row `favourites` (`dao.FavouritesGroupID`): `EnsureFavourites` inserts it,
ignoring the duplicate, on every listing and before the first heart, so no migration; `ListImageGroups`
orders it first; `ImageGroup.favourites` marks it and the device names it in the request's language
(`dev.files.favourites`; the stored "Favourites" is only what an older app shows); `RenameImageGroup` and
`DeleteImageGroup` on it answer `dev.files.favourites_fixed`. The heart's requests are `GetFavourites`
(paths in, the paths whose photo is a favourite out, `resp_favourites`) and `SetFavourite` (paths in or out,
Ack); a device before them answers `unknown_payload` and the apps show no heart. Members are photos as in
any collection, so Add to collection and Remove from collection work on it unchanged (`TestFavouritesMySQL`,
`websocket/favourites_test.go`). **A person's face** (owner, 2026-10-31) can be chosen: `SetPersonCover`
takes one of their photos (`hash`: that photo's face of theirs, the biggest; `dev.files.person_cover_no_face`
otherwise), a picture of the owner's own (`image`: the apps' square crop, a JPEG of at most 256 KB, stored
encrypted like the faces' thumbnails in `people.cover_image`; `dev.files.person_cover_image` otherwise) or
neither (the device's choice again). `people.cover_chosen` (release 157) keeps a chosen face as more photos
of them come in (`SetPersonCoverFace` updates the cohesion only); a chosen face gone (the photo deleted, Not
this person) is the device's choice again, a picture stays. `ListPeople` shows the picture, else the cover
face, and says `Person.cover_chosen` (the apps' Choose automatically). Older devices answer
`unknown_payload` (`TestPersonCoverMySQL`, `websocket/person_cover_test.go`). The face the device chooses
(owner, same day: "the best looking one") is the best-looking of theirs that is clearly them
(`files_manager/face_covers.go`): `faces.quality` (release 157) is `images_quality.FaceQuality` of the
crop when found - sharpness, the box's size, light, and frontal (the crop's halves mirrored) -;
`dao.BestFaces` ranks a person's measured faces by 0.8 of it and 0.2 of their photo's NIMA score, and
`chooseCoverLocked` keeps the first whose average similarity to their references reaches the same-person
threshold and 0.85 of their cohesion, else the medoid as before. It runs when a new face changes the
references or looks better than the chosen one (`coverMayChange`); the faces found before are measured
once after a sign-in (`BackfillFaceQuality`, Background work "faces", `settings.face_quality_done`), then
everyone's face is chosen again (`TestBestFacesMySQL`, `face_covers_test.go`). **Rating** (owner,
2026-11-02; release 163's `updates/163.sh`; device side `files_manager/rating.go`, `dao/ratings.go`,
`websocket/rating.go`): `image_groups.rating` turns it on for a collection (off keeps the ratings).
Each one who rates keeps their own rating of a photo (release 169's `updates/169.sh`:
`photo_rating_sources`, one row per (collection, hash, source) - source `""` the owner's, else the
link's uuid): a link's visitors see and change only their link's (`dao.LinkRatings`) - until then one
row was shared by the owner and every link, so a client saw another's comments and the owner's own
notes, and could overwrite or clear them. `photo_ratings` stays what the owner sees and the searches
use, one row per (collection, hash): the latest change of anyone's, its `link` whose (`from_link`),
set in the change's own transaction (`dao.SetPhotoRating`). A link taking theirs back (0 and "")
deletes its row and, when the owner saw it, shows the latest left; the owner's clear stays a row with
neither, which hides the older ones from the owner; sources keep their time to the microsecond, so of
two changes in one second the later is the latest. The apps show the owner that one rating per photo,
as before. The stars are plain (searched and sorted like tags' scores: `searchMediaRatedClauses`, a
left join on the member `ig`, ranked); a comment is sealed to the user's X25519 key pair
(`vault.rating_public`/`rating_private`, the private half under `ses.Encrypt`, made at the first
sign-in that finds none; ephemeral ECDH + HKDF-SHA256 + AES-256-GCM, bound to group and hash), and a
link's also for its visitors while the owner is signed out (`link_comment`) under a key made from the
link's own secret (`linkCommentKey`, HKDF; `cSealLinkV2`), which only its visitors' requests present
and the device never keeps - one stored before release 169 (`cSealV1`) under the collection's link
key (`image_groups.rating_key`, sealed with the owner's key, still made with the collection's first
rated link and carried in each rated gallery's sealed manifest as `rating_key`, where having one says
the link takes ratings). The owner's own comments are never sealed for a link (release 163 copied
them under the collection's key at its first rated link). At each start `dao.ReconcileRatingSources`
gives every rating stored before - or by an older binary put back by an update that didn't start -
its source's row (a link's comment from `shared_comment`, which nothing writes any more) and drops the
owner's comments' copies. A rating row goes with the photo's membership (same transaction, every
source's) and with the collection; it is inserted from the membership row, so a photo taken out
meanwhile leaves none. Rated galleries record each item's hash in the manifest and `shared_links.group_id`/`rating`;
`RateSharedGalleryItem` is public (per-address and device-wide token buckets, `visitorRatings`) and
alerts plus pushes at most once an hour per link (`shared_links.rating_alerted`, a conditional
update; a `NotificationRating` row through `dao.AddRatingNotification`: never grouped, its keys rendered
in the reader's language like any alert's, `notifications.group_id` the collection a tap opens, its
title in `actor_name` too for apps from before the type; the push names no collection).
On the web the heart is `MediaViewer`'s (`canFavourite`: Images, Files, On this
day's See all, Places), before Download; its rules are `favourites.ts` (`node web/scripts/favourites-test.mjs`; the
page's store in `useFavourites.ts`): one `GetFavourites` for the photo and its neighbours (a burst of steps asks
where it stops), answers kept by path for the page (still shown but asked again at each viewer opening, forgotten
after Add to or Remove from collection on Favourites), no heart in its kept place until the device has answered,
a press shown at once (one `SetFavourite` per path at a time; refused, back with the device's words or
`favourite_failed`; `refuseOnDemo` first, `reqSetFavourite` in demo.ts), F presses it, a pop as it fills (none under
reduced motion), `unknown_payload` no heart for the page's life. Collections lists it first in either Sort by
(`sortCollections`), a heart for its empty cover (and in Add to collection); open, it has no rename or Delete;
photos emptied in its viewer stay there and leave its grid as the viewer closes (`knownOut`, `dropDeleted`), and a
viewer that changed a heart lists the collections again as it closes (`groupsChanged`). Among one person's photos
(exactly one person in the filter, tags allowed) the bar also has **Not this person** and **Someone else…** (`personFix.tsx`, the note in `personFixDone.ts` -
`node web/scripts/person-fix-test.mjs` -, `RemoveFromPerson`): the first asks (`ConfirmDialog`, ember -
the photos stay), the second lists the other people (`usePeople`, faces and names, a name field past 8)
and moves the photos to the one picked; they then leave the grid as deleted ones do (`dropDeleted`), with
a note, and the people are listed again (the header's face may have been one of them). The viewer has both
for the photo open (`MediaViewer`'s `personId`/`onPersonFixed`, round buttons before Delete), moving on as
after its Delete. **Use this face** joins them in both for exactly one photo, not a video (`faceHash`,
`personCover.ts`, `node web/scripts/person-cover-test.mjs`); the person's face atop their photos is a
button (`PersonFace`) whose menu has Upload a photo… (the file chooser, then `PhotoCropDialog` with the
face's title and hint) and, with `coverChosen`, Choose automatically. Each sends `SetPersonCover`
(`libraryStore.ts`' `choosePersonCover`, `refuseOnDemo` first), says `cover_done` or the device's words,
and lists the people again. With them the bar's labels give way to icons below 1440px, 1680px with Remove
from collection too (Use this face counted even unshown, so a second pick doesn't relayout the bar;
`barCompact`, `.pg-selbar[data-crowd]`). **A photo's faces** (release 159's `GetPhotoFaces`) are in every
`MediaViewer`'s Info, under the details once those are in, so nothing above them moves (`photoFaces.ts`,
`node web/scripts/photo-faces-test.mjs`): asked while Info is open on a photo or a video (a video's
people added by hand; an older device answers none) - never while `faceRecognition.ts` says off - and
after a face's × or an Add; nothing until the device answers, none while it says face recognition is
off, and none for the page's life after `unknown_payload`. A row is the crop (a blank
circle without one) and the name or Unnamed - one button, `face_photos` its tip, that closes the viewer
and opens that person's photos (photoFilter's `showPersonPhotos`: the menu's Images, then `showPerson`;
Images already on exactly them keeps its grid) - and an × (`not_this_person`, described by the name):
`refuseOnDemo`, then `RemoveFromPersonDialog` with the face's name and the answer's hash. Opened on that
person's photos it is the viewer's own Not this person (`onPersonFixed`: the photo leaves the grid);
elsewhere the photo stays, its faces are asked again, the viewer's note says `removed`, People is listed
again, and from the keyboard the focus goes to the next × (else Add). **Add a person** (2026-11-09,
`AddToPerson`, envelope 170): with the answer's `adds_people` (never while face recognition is off) and a
hash, the block shows even with no face and ends with a row-like button (`face_add`, `AddPersonIcon`
in a dashed gold circle, `.pg-info-face-add`): `refuseOnDemo`, then `AddToPersonDialog` (`personFix.tsx`;
Someone else…'s list is now `PickPersonDialog`, shared by both): People but for everyone in the answer
(`notInIt`), titled `face_add_title_photo`/`_video`, `face_add_none`/`face_add_all` when it offers no one,
errors in it (the device's words, else `face_add_failed`; the bridge's answer or none, `error.unreachable`).
On the Ack it closes, the item's faces are asked again (by-hand rows come after the faces, in the order
added), the focus goes to the new row once that answer is in (else Add) and People is listed again;
stepped to another item meanwhile, only People is. A by-hand row (`by_hand`: no box, their People face
as the crop) opens their photos like a face; its × is `common.remove` and takes them off at once, no
question (`RemoveFromPerson` to "": face recognition learnt nothing from them), then as after a face's
× (the note says the device's words, else `remove_failed`, if refused). **A collection's Rating on the web** (release 163;
rules in `rating.ts`, `node web/scripts/rating-test.mjs`; UI in `RatingPanel.tsx` + `Rating.css`, words in
`ratingText.ts`, texts `web.images.rating.*`): an open collection's header has a Rating button before Share
(a star, filled while on; named "Rating on"/"Rating off"; `SetImageGroupRating`, `refuseOnDemo` first; the first
time in a browser the `turned_on` toast, `otc_rating_hint_seen`; `unknown_payload` = `needs_update`). While on,
the header has Show (All photos, 5 stars ... With a comment; away from All it is drawn ember) and Sort by (Date /
Rating), `photoFilter.rating` - back to the defaults on another collection, leaving it, or Rating off -, sent as
`SearchPhotos.rating`/`by_rating` (`ratingSearch`: nothing at the defaults); either away from its default makes
`isDateOrdered` false (no months, buckets or scrubber); Show leaving nothing says `no_match` with Show all
photos. Tiles show `RatingBadge` (bottom left: "★ 4" and a bubble for a comment; the button's
`aria-description` says it). The viewer (`MediaViewer`'s optional `rating`, Images in a rated collection only)
has the stars (toggle buttons in a group, one Tab stop, arrows/Home/End move between them and never page;
the pressed one again clears) and the comment box with Save (Ctrl/Cmd+Enter; an unsaved comment is saved as the
viewer moves on or closes) - a bar under the photo, or below 600px wide or 560px high a sheet the header's
Rate button opens (`useRatingSheet.ts`); "From the shared link" while `from_link`. `createRatings` shows a
change at once and sends one `SetPhotoRating` at a time per path (a refusal goes back, in the viewer's note or
a toast after it closed; `rating_off` lists the collections again); a ranked grid is asked for again as the
viewer closes when something was rated. Share as gallery has "Let people rate the photos" for a collection
only (`CreateSharedGallery.rating`, off by default; the collections are listed again after), Settings › Shared
links says `rating_on`, and the `/shared` page of a link with `SharedGallery.rating` says `rating_intro`, has
the badges, the viewer's stars and box (`RateSharedGalleryItem`, no from-link note) and its own Show and Sort
by over its items (`arrangeItems`; its viewer steps through the order it opened with). Alerts show a
`NotificationRating` row (a gold star, the device's title, its details under it, the time) that opens its
collection in Images (`libraryStore.findGroup`, listed again first; gone: `web.social.alerts.collection_gone`
under the row). **Slideshow on the web** (2026-11-06; owner: "go to a collection and set it in show
mode ... an image after another waiting 5 seconds per image, the transition in between images should be
nice", then "the order should be random" and the same for the whole library; `Slideshow.tsx` + `.css`, a
lazy part App mounts from `slideshowState.ts`; rules in `slideshowRules.ts`, `node
web/scripts/slideshow-test.mjs`; loading in `slideshowLoad.ts`; texts `web.images.slideshow.*`): an open
collection's header has Slideshow (a play triangle, before Rating, while `fileCount` > 0) and Images'
top bar has it while nothing narrows Images (`isFiltered`: the whole library). The order is the device's
`RandomPhotos` (device notes): `createRounds` takes its pages (20, small thumbnails) a seed a round
(16 hex digits), every photo once a round, then a new seed; a new round never starts with the photo just
shown (second, or held back until its round has another). Each photo: 5 s (`cPhotoMs`), the next fading
in over it in 1.2 s (`cFadeMs`, the layer's `ss-fade`; what it covered goes at `animationend`), a Ken Burns
move over 7.4 s (`kenBurnsOf`: six moves in turn, zoom 1-1.08, pans of 1-1.5%, in and out; CSS custom
properties in `ss-kb`), shown whole (`object-fit: contain`) over a 96 px cover-fit copy blurred and darkened
by the canvas (`ctx.filter`, checked by drawing; else CSS `filter`) from the page's small thumbnail.
Reduced motion: the fade only. The photo is the viewer's full size (GetFile through `fullSizeCache`, an
unsafe type left out), else the big thumbnail (GetThumbnails), decoded with `createImageBitmap` (EXIF
turned) and redrawn as a JPEG at the screen's size times 1.08 (`fitSize`: never larger than it is),
then `img.decode()`d with its backdrop before its turn; the next is loaded as one goes on screen, and
when its 5 s are up before it is ready the photo stays until it is. One with neither is skipped
(`SlideGone`); no answer or the bridge's is `SlideAway`: asked again after 2 s doubling to 30 s, at once
when the device answers or the browser is back online - the show stays on what it has, and App keeps the
`device_unreachable` cover off while it is open (a switched-off account or no storage still cover it).
Slides are kept for the photo before, the one on screen and the next (blob URLs revoked). The overlay
(z-index 3000) asks for full screen (`requestFullscreen`, a refusal ignored; leaving it closes the show)
and a screen wake lock (asked again when the tab shows); Back closes it (`useBackLayer`), and the focus
goes back. Controls (`.ss-chrome`: "3 / 24" from the round's `random_total`, Close, Previous, Play/Pause,
Next; 64-76 px) hide while it plays and come back 3 s for a tap, a click or a mouse move, staying while
paused; Space, the arrows and Esc are taken in the capture phase; a swipe (`swipeOf`: 60 px, mostly
sideways, under 0.9 s) steps. Checked in muted headless Chrome (2x, 1024 x 768, CDP mouse, keys and
touch) against a Docker test device built from this tree with the demo library and its Favourites: a
change every 5000-5003 ms, two layers half a second into a change, the portrait photo's fill, pause (the
move stops too), Next/Previous/arrows/swipes, past the last photo into round 2 and 3 ("12 / 12" then
"1 / 12"), Esc back to the collection as it was, reduced motion, the library's ("1 / 116"), and the
container paused 20 s (the show on what it had, no cover) and back. What Images shows is `photoFilter.ts` (tags, people, open
collection, a place's area from Places; a store the search, People, Collections and Places pages and the grid share); the people, collection
and tag lists are `libraryStore.ts` (cover URLs made when a list arrives, never in a render). Images
(`PhotoGallery.tsx`) groups date-ordered photos into month sections, Google Photos-style selection (hover
or long-press, a drag for a run - see below - numbered in post order) with a bar over the top bar, dialogs for posting and collections,
and `DateScrubber.tsx` (a column of years with a mouse, a draggable handle on touch). `People` and
`Collections` are pages of their own (`PeopleView`, `CollectionsView`), each with Sort by over its
grid from two items on (`SortBy.tsx`, a native select; rules in `librarySort.ts`, `node
web/scripts/library-sort-test.mjs`, the phones' rules; owner, 2026-10-30; it survives Sign Out): People's Most photos (the
default, the order the page always had: named people first, then the unnamed, each in the device's
order) or Name (A to Z in `nameCollator(t.lang)` - case and accents ignored, numbers by value, spaces
trimmed - names that compare equal and the unnamed in the device's order, the unnamed last);
Collections' Newest first (the device's order, the default) or Name (the untitled last). Kept in
`otc_people_sort` / `otc_collections_sort` (a storage that throws: for the visit only). Only the
pages follow it: the Someone else… and Add to collection pickers keep the device's order, as on
the phones. `web/src/net/` holds the WebSocket/proto
client; `web/src/views/` are top-level routed pages (`SignIn` - a new device's setup - and `Social`); `web/src/components/` are the
feature widgets (files explorer, photo gallery, friendships, settings, status, profile, social feed —
each with a co-located `.css`). Built output (`vite build`) is copied by `make web` into the device's
own static dir and the iOS app's bundled web assets. The bridge does **not** get its own copy (issue
#95): a browser hitting `<device>.off-the.cloud` for a static asset is proxied straight through to that
device over the same bridge tunnel every other request uses (`ReqGetStaticAsset` /
`staticassets.Resolve`, shared with the device's own direct HTTP static handler) rather than served from
a separate bundle the bridge would otherwise need redeployed by hand on every web change — this is what
lets a device running an older build still work correctly through the bridge. The bridge's *own*
pages are deployed by `bridge/makefile` independently of a device's web build. The public landing
page, the account page and the pages shown for a disabled or unreachable device are html/template
files in `bridge/site/pages/*.html`, embedded in the binary (package `bridge/site`) and served by
`bridge/api/site.go` in the visitor's language: `/` and `/account` (English, or a 302 to
`/<code>/` / `/<code>/account` by `Accept-Language`, query kept), `/en/`, `/en/account`,
`/<code>/` and `/<code>/account`, `/account.html` -> 301 `/account`, `/privacy.html` and
`/terms.html` -> 301 without `.html`, a page's address with a last slash (`/account/`,
`/es/private-social-network/`, `/privacy/`) -> 301 without it, hreflang plus x-default (and `pt`
beside `pt-PT`, the same page), a footer language switcher once a second language ships, and a
CSP with a nonce per response (docs/i18n.md "bridge"). An address the host doesn't have is the
site's own 404 page (`notfound.html`, noindex, in the path's language or `Accept-Language`'s) for
a browser asking for a page (`Accept: text/html`), the bare 404 for anything else. They are never read from `/var/www`, where older deploys left copies (an
old `account.html` there is never served). `bridge/static/` holds privacy, terms, the admin panel
and the assets, copied to `/var/www` by the deploy - and Raffle Numberer's support and privacy pages
(another app of the owner's, a Mac app in English and Spanish, which App Store Connect links to):
`raffle-numberer.html` at `/raffle-numberer`, `raffle-numberer/privacy.html`, and the Spanish ones at
`/raffle-numberer/es` and `/raffle-numberer/es/privacy` (`/es/...` is the site's own: a 404 there).
Plain static files, in no sitemap or llms.txt. For search: the
three comparison pages (`social.html` at `/private-social-network`, `alternative.html` at
`/google-photos-alternative`, `drive.html` at `/google-drive-alternative`, each also under
`/<code>/...`, routed like `/account`), the Raspberry Pi page (`pi.html` at
`/raspberry-pi-home-server`: what it needs, the setup, the mirrored drives, the lights, a new Pi;
linked from the hardware answer and the footer, its claims the landing page's FAQ's) and the
client proofing page for photographers (`proofing.html` at `/client-proofing-gallery`: a shoot
shared as a gallery link with Rating on, the client's stars and notes, Show and Sort by in the
collection, the link's 1/7/30 days, small copies or originals; release 163's Rating, its claims
the Rating spec's only - no watermarks, payments or print orders; linked from the landing page's
`#photographers` section and the footer; `proof-sheet` in common.html with `pages/proof.css`
draws three sample-library photos, `img/proof-{1,2,3}-{360,720}.webp`, with stars and notes) share the
landing page's head, header and footer (`pages/common.html`, `pages/site.css`, plus
`pages/compare.css`); their head carries Open Graph tags and one JSON-LD graph (`bridge/site/head.go`:
the same `@id`s in every language, nothing the page doesn't state - no ratings, counts or
versions, and no person: the Organization has no founder; `og-1200x630.jpg` is the hero photo's
crop); the account page says noindex. Titles are at most 60 characters and descriptions 155 in
every language (the keys' `max`); the comparison pages' titles leave out the brand, which search
engines show beside them. `/robots.txt`, `/sitemap.xml` and `/llms.txt` come from
`bridge/site/crawl.go` (llms.txt rendered from the English catalog, its keys in `llmsText`, each
question linked to its own answer), and `/llms-full.txt` from `llmsfull.go`: every public page in
English, rendered and read back into Markdown (its `<main>`; no pictures, scripts or forms), made
once per origin. Every FAQ question is a `<details id="faq-<key>">` (`faqID`:
`site.landing.faq.social_how.q` is `#faq-social-how`) that common.html's `faq-script` opens when
the address names it. The header links the account page in the page's language (`.Account`, and
`localHref` for the texts' `account`/`signup` tags); below the width its links need (1180 px in
English, 1300 px otherwise: measured in Chrome with the fonts loaded, plus a scrollbar's room -
measure again when a link is added or renamed) they move into a Menu, a `<details>` with no script, and on a phone
(480 px) "Create an account" too. It stays at the top of the whole page (a `body` only as tall as
the window let it go after the first screen), and `scroll-padding-top` keeps a `#section` below
it. `site.css` carries `bridge/static/fonts/fonts.css`'s `@font-face` rules word for word
(`TestFontFaces`) and the head preloads Unbounded's and Manrope's latin files, so the public
pages don't wait for a stylesheet; the account, privacy and terms pages still link `fonts.css`.
Pages and those files are gzipped, and so are the static directory's text files (`compress.go`:
`fonts.css`, privacy, terms, `sw.js`; never on a Range request); unknown addresses on the
bridge's host are 404 (never the old web app's `index.html` left in `/var/www`); `www.` is a 301
to the bare domain; every answer for a device's address says `X-Robots-Tag: noindex`
(`noindexDevices`). Static files: content-named fonts cached for good, a picture asked for with
its `?v=` 30 days, other pictures and `fonts.css` a day, HTML `no-cache`; the static pages
(admin, privacy, terms) are never framed and load nothing from elsewhere (`setStaticPagePolicy`:
`X-Frame-Options: DENY` and a policy with `frame-ancestors 'none'` - a device's page is the same
site, so the admin panel's Strict cookie would ride along into a frame there; the panel's allows
its inline script and the bridge's API, the others run nothing). A device's web app
files, relayed by `proxyStaticAsset`, go out with `X-Content-Type-Options: nosniff` and
`Content-Security-Policy: frame-ancestors 'none'` only (a fuller policy would have to fit every
web app build a device may still run; the device types each file by its extension), and text
gzipped for a client that takes it; a content-named one (`cHashedAssetPath`) is compressed once,
kept by the SHA-256 of the device's bytes (never by its name: another device's file under the same
name never stands in for it) in a 32 MB least-recently-used cache. The landing hero puts
`img/hero-{900,1280,1600}` (WebP and JPEG, from `docs/promo/pi-photographer-desk.jpg`) left of the
summary, stacked below 820 px; its install block (`#install`) has three tabs, the ARIA tabs
pattern (arrows, Home and End select; only the selected tab is in the tab order; chips below 600
px): "Computer app" (first,
selected, "Recommended") - the Mac App Store, Windows and Linux buttons are the Downloads
section's links (`TestInstallTabs` keeps them equal; the visitor's OS is ember and first) and its
steps say only what issue #184 below does (the Mac hands the card to Raspberry Pi Imager and can
finish over Bluetooth; otc-sync writes the card and the phone app finishes) - then "Raspberry Pi
5 image" and "Install script" (`/#script` opens it); the header's "Get started" goes to it, and the
section under the hood (`#get-started`) is the install script's. The gallery (`#screenshots`) has
the device photo, the Mac popover (`macos-sync.jpg`), five phone shots (`mobile-1..5`) and six web
shots (`web-1..6`, three to a row), each shown as a WebP thumbnail at two widths, about once and
twice its size on the page (`mobile-N-{180,360}`, `web-N-{450,900}`, `device-{460,920}`,
`macos-sync-{340,680}`; `cwebp -q 80 -m 6 -sharp_yuv -resize W 0` from the full picture), with
`srcset`/`sizes`, opening the full picture (`data-full`) in the lightbox, which makes the page
behind it inert and keeps Tab on its buttons; the JSON-LD's screenshots are full pictures
(`TestStructuredData`, `TestLandingPictures`). No picture shows a real person (the AI sample
library of `docs/promo/source/library`, animals, or nothing). A picture that replaces another
keeps its file name, because the deploy copies over `/var/www` and never deletes, so the old file
is overwritten. It, and its thumbnails made again, also get a new `?v=` in the page. The
Printable section (`#printable`, after the gallery; the header's "Printable") shows the two cases
of `hardware/` (Cases to print, below) - a render each, `img/case-{cloud,screen}-{600,1200}.webp`
(OpenSCAD's Tomorrow Night scheme with its background swapped for `--ink-2` by ffmpeg's colorkey,
the commands in the page's comment), what it is, a "Files and instructions" button to its folder
on GitHub, and the CC BY-SA 4.0 line (tag `cclicence`); its claims are the cases' READMEs'.

Web invariants: never call `URL.createObjectURL` during render. PhotoGallery and NewPostPicker use
`useObjectURLs(items, make)` (`make` module-level and stable); other URLs are made in a memo or
effect and revoked in its cleanup - only `blob:` URLs, never a `/media` stream URL. Social's `Post`
is a module-level memo component: keep its callbacks stable and `highlighted` a boolean, or every
post re-renders. Infinite-scroll grids use `usePageRetry` (1 s doubling to 10 s; user searches
`reset()`). Files reads non-media with chunked `ReqReadFile` (`readAll`, which is
`mediaDownload.readPieces`, as the viewers and downloads: a piece with no answer, or the bridge's,
is asked again at the same offset after 1, 3 and 6 s; a file replaced mid-read is read again) and
uses `GetFile` only for what the device converts. `useWS`: `lastAuthRef`, the
password replayed on reconnect, is set on an ok Ack (or when nothing answered and nothing verified
is held) and cleared when the device refuses it; a typed password that met a lockout is not replayed,
and one typed on the page (`signInWithPassword`: `keepOnlyIfAccepted`) is never kept after no answer -
the person presses Enter again; with `refuseNewDevice`, a device whose GetPubKey answer (the same one
the password is encrypted with) says it is new gets nothing (`NewDevice`), so it never adopts a sign-in
as its owner password and its setup shows instead;
call `useWS.passwordChanged(newKey)` after a ChangeKey; the bridge's `device_unreachable` is not a
refused sign-in (`authOwed`); only a tab that has signed in (`hadSession`) redeems the stored token
on reconnect. Sign Out also clears `otc_files_path`, `otc_photo_search_tags` and a pending
language change (`otc_language_pending`; `otc_language` stays). `net/`'s errors carry translated
messages: test for a request with no answer with `err instanceof NoAnswer` (`net/ws.ts`), never
by its text - `NoAnswer.sent` is false when it never left (no socket, or the sign-in useWS waited
for failed), true when it went and nothing came - and import `t` there from `../i18n/runtime`,
never `../i18n` (a cycle through the
provider and `useWS`). `web/` has no test runner
(`node web/scripts/i18n-test.mjs` tests the i18n runtime and ws.ts's `lang` line through Vite's
SSR loader, `net-test.mjs` the socket's clocks and the password's sealing); `npm run lint` already
reports about 120 problems (mostly `no-explicit-any`), so compare counts before and after a change.

Holding up on the web (the 2026-10 review). Loading: `main.tsx` is only a tiny entry - `/shared`
imports `sharedEntry.tsx` (the gallery, its socket set as App sets it), everything else
`appEntry.tsx` (App): a shared gallery's visitor never loads the app. `index.css` is linked from
`index.html` (its colours from the first paint), and node-forge is its own chunk (`forge-*.js`,
`vite.config.ts`) loaded only where `crypto.subtle` is missing (a plain-HTTP page at home):
`pwCrypto.ts` seals with WebCrypto RSA-OAEP everywhere else, so `encryptWithPubKey` is async. The
TS bindings have no `fromJSON`/`toJSON` (`outputJsonMethods=false` in the makefile's
`ts_proto_opt`). `index.html` carries a Content-Security-Policy (scripts, styles, workers, fonts
from `'self'`; pictures and media also `blob:`; `connect-src 'self' ws: wss: https://off-the.cloud`;
`frame-src` OpenStreetMap; no objects, no `<base>`): the dev server strips it (`vite.config.ts`),
and a new origin the app reaches must be added there. The device's static handler adds `nosniff`,
`frame-ancestors 'none'` and `X-Frame-Options: DENY` (`api/api.go` `serveStatic`). Errors: every
section is inside an `ErrorBoundary` (in its own component, `where="tab"`; SettingsForm only
inside the app's, `appEntry.tsx`): "Something went wrong" with Reload and Go to Images in place of
the section, the top bar and menu staying. A section moves to another without App through
`showSection.ts` (it presses the menu's own item, found by its `data-tour`; with no menu it saves
the last tab and reloads). The socket (`ws.ts`): a request waits 60 s, 5 min for a transfer
(`cTransfers`: its clock waits while this browser's bytes still leave), and work the device
finishes before answering has no clock of its own (`cOpenEnded`: a post's videos, an update, the
disks, a network); past its time with no frame since it went, or 20 s of silence while only small
requests wait (`cQuick`), the socket is dropped - never while a transfer may be arriving - and
the next request opens another; an opening that hangs gives up after 15 s. The polls (storage
status, the Alerts badge: `devicePoll.ts`) go through `request()`, which reconnects, backing off
to a minute; `useWS` asks for the status again on `online` and when the tab is shown, and replaces
the stored token every 30 min while signed in. Feedback: Images' toasts and link dialog are shared
(`useToasts.ts`, `ToastView.tsx`, `LinkDialog` in `ConfirmDialog.tsx`) - Files says with them what
a share, an open, a version's download or a delete came to (the device's words), deletes with its
progress on the button, and keeps a download's `blob:` URL 40 s. Settings' results are a fixed
toast in two always-present live regions (`role=alert` for an error, which stays until closed).
Social: a post with no files is a card of text; the first page has a spinner, then the device's
words and Try again (`usePageRetry`); a comment stays in its field until the device has it, and
then only that post comes again (`refreshPost`, `ReqGetPublication`, keeping a like made
meanwhile); its photos are buttons with alt text (`web.social.viewer.photo_by_position`); a blob
URL is made only for a type `inlineTypes.ts` allows (`safeImage`/`safeVideo`, Files'
`INLINE_MIME`), anything else is download-only in the viewer. Images drops a thumbnail's bytes once
its Blob exists (`thumbOf`'s WeakMap; the post dialog reuses the Blob) and lets the browser skip
months off screen (`.pg-sec` `content-visibility`, room from `--pg-sec-rows`); its empty library
uploads to `/Uploads/` (`uploadAsNew`: `name (2).jpg` when taken). Files' list rows are memoised
(`FileRow`, actions behind a ref) in blocks of 100 with `content-visibility` (per row it cost as
much as it saved), and the grid keeps at most 1,000 thumbnails, the least recently seen going
first. The list's columns (rules in `filesColumns.ts`, `node web/scripts/files-columns-test.mjs`;
the page side `useFilesColumns.ts`) are one grid template on the table, `--fb-cols`, with the
hidden ones in `data-hide` - written to the DOM, never React state, so a drag draws no row. Name
takes what the others leave and never less than 10 characters plus its icons (a long name gives
way to them down to that, then the versions pill); as the list narrows Created hides, then
Modified - it never scrolls sideways, which would unstick the headings. Each heading but the last
has a handle at its right edge (`role="separator"`: pointer capture, the arrow keys, a double-click
puts that column back, Name's every column); widths stay in `otc_files_columns`. The composer
uploads one file at a time with its progress, stops when some failed to ask whether to post the
rest or try again, and never sends a file twice. `/shared` fetches the thumbnails of the tiles in
view, four at a time. The viewer's map loads on Show on map, with no Referer and sandboxed.

The second pass. Back (`components/backStack.ts`, `node web/scripts/back-stack-test.mjs`; the only
file that writes the history - `replaceAddress` for a changed query): a section moved to (App's
`goTo`: the menu, the top bar, a link in a page) is an entry, one App sets by itself (a sign-in,
a failed restore, People without faces, Images back to Collections) rewrites the entry it is on;
every layer calls `useBackLayer(open, close)` - the viewers (MediaViewer, the feed's, Files' and
/shared's), `Modal` (so every ConfirmDialog and LinkDialog), the composer, the trimmer, the
drawer, the tour, People's and Settings' dialogs - and Back closes the top one (`close` returning
false keeps a dialog at work: the Back stops there, however many steps it went, the layers under
it and the section stay, and history goes forward to its entry again), else shows the entry's section
(`sectionFor`: signed out, the landing page). A layer closed by its own button leaves its entry
to the next one written at that moment (a pick in the drawer is one entry) or steps back off it;
a Back that changes nothing goes on back. Entries keep the address (`#download`, `/shared#…`) and
say `{otc: {doc, i, tab, layer}}`; none in `__OTC_CONFIG`. Lazy parts (`lazyPart.ts`, `Part.tsx`):
Settings, People, Files, the composer, the viewer (and the app's unused /shared view) are chunks
of their own, behind a spinner, and a chunk that didn't come is "This part of the app couldn't
load" with Reload (a layer's with Close too): a first load of the app is 777 KB of script (193 KB
gzipped) instead of 979 KB (249 KB). A restore that got no answer keeps "Signing in…" and tries
again (`net/sessionRestore.ts`, `node web/scripts/session-restore-test.mjs`: 1 s doubling to 30 s,
at once on `online`, the tab shown or the unreachable screen clearing); only the device's refusal
lands signed out. A device that can't reach its storage (issue #207: error_code
`storage_unavailable` on every answer, GetPubKey first) is a `deviceStatus` of its own (ws.ts passes
the envelope's `error_code`): the unreachable screen with a disk, `web.shell.unreachable.storage_*`
and the device's words, no Wi-Fi hint, GetPubKey every 30 s (`recheckMs`) and at once on `online`
or the tab shown; any answer that isn't the code ends it. It is no verdict on a credential: useWS
throws `StorageUnavailable` and keeps the password and the stored token, and a restore waits 30 s
between tries (`waitAfter`) - `node web/scripts/storage-unavailable-test.mjs`. While the socket is
down (`wsClient.isDown`) a "Reconnecting…" pill shows under the top bar
after 1.5 s (`ReconnectingBanner`), not over the unreachable screen. Notifications are asked for
only from a click: the feed's `PushCard` ("Get notified when family posts", Not now remembered in
`otc_push_card_dismissed`) while `pushUndecided()`, and Settings' switch. Above 500 MB
(`bigFile.ts`) a download put together in memory (a ZIP link, a non-media file Files saves or
opens, a shared gallery's originals) asks first (`BigFileDialog`: "This is 1.2 GB; …"). Leave the
bridge asks in a ConfirmDialog. The composer's trimmer streams a library video (GetMediaURL),
else reads it in ReadFile pieces, as the viewer reads a clip the device won't stream: the web
never asks for a whole video with GetFile. `net/media.ts`' `askStreamURL` tells the answers apart
(`node web/scripts/stream-url-test.mjs`): a URL; none (not worth streaming: pieces); a device
before issue #110 (`unknown_payload`: pieces, asked first above bigFile.ts' 500 MB); or a failure,
said in the device's words with nothing read - never a fall back to reading a long video whole.

Holding on through a slow device or a dropped connection (2026-11). The unreachable screen
(`DeviceUnreachable.tsx`) is a modal `<dialog>` over the app, which stays mounted and inert under it:
a composer's caption and picks, an open dialog and a page's state are there when the device answers
again, and the focus goes back where it was. For `device_unreachable` it comes after 4 s
(`deviceStatus.coverAfterMs`; App's `useCover` asks GetPubKey at 1.5 s), so a blip that mends itself
never shows it; `account_disabled` and a device without its storage show it at once. Escape never
closes it (a second one the browser takes as closing it brings it back) and its keys never reach the
page; nor does Back while the device is away: it is a cover (`useBackCover`), so the browser's Back,
Alt+Left or a back swipe, one step or several, never closes the composer under it or changes the
section - unless nothing of the page's lies under it (no other layer, and it came up over the entry
the page loaded on), when Back goes through it and leaves the site as it would without it. A
switched-off account's has none (that lasts until the owner acts): Back works as usual. Its words are the web's own (`web.shell.unreachable.text`, `.disabled_text`): the bridge writes
those two codes in English only. `deviceStatus.isBridgeAnswer` tells the bridge's answer for the
device from the device's own - every request that follows treats it as no answer, never as a refusal
in the bridge's words. ws.ts takes those words out of it as it arrives (`withoutBridgeWords`, its
code kept), so no part of the app can show them: each says what failed in its own (Files' listing
"The device did not answer.", a sign-in the same or "This account has been disabled"). Nothing mounts again as the screen lifts, so a load that runs once and failed
runs again when the device answers again or the browser is back online (`useDeviceBack`, on
`deviceStatus.whenDeviceBack`, which counts a device back before anything listened too): Files'
listing, Settings and its reprocess status, Users, the profile and the account button's, Alerts,
Friends, Shared Links, Bridge, Wi-Fi's first look, Update's check, Tailscale's status and the role
that shows Tailscale, Update and Logs (`useInstanceRole`; the bridge's answer used to hide them).
Grids that page have `usePageRetry`; the library's lists ask again while they show. A post whose answer never came (`postConfirm.ts`, `node
web/scripts/post-confirm-test.mjs`): unsent (`NoAnswer.sent` false) says nothing was posted; went and
unanswered keeps the composer with a note, "Post again" on its button, and looks for the post among
the newest four (the owner's own, none there as it went - those the feed showed, Social's
`knownPosts`, and the newest four the device had besides, asked just before it went, 5 s at most:
`postsBefore` -, the same caption, no more items than were sent, made no earlier than 15 minutes before it was sent) at 3, 7 and 15 s, then every 30 s and at once when the device
answers again, closing as posted once it is there. Users' create and delete that went unanswered say
the device may still be at it and list the users again, then 10 s on. Uploads from the web resume
(issue #205's rules): a file of more than one 4 MB piece is hashed first (`net/sha256.ts`, fed a piece
at a time - crypto.subtle hashes only a whole buffer and is missing on a plain-HTTP page) and a piece
that fails is begun again with the same key after 2, 4, 8, 16, 30 and 30 s (`net/upload.ts`, `node
web/scripts/transfer-test.mjs`). Files' uploads are one queue, a row each by id (waiting, reading,
sending, done, failed with the device's words), sent one at a time from a drop or the Upload button
beside Sort by (the browser's file chooser: phones, the keyboard and screen readers have no drag);
a dropped folder's files go under their paths, hidden names left out. The queue is the page's
(`filesUploads.ts`, `node web/scripts/files-uploads-test.mjs`), not Files': it goes on while another
section shows (App unmounts Files) and Files shows its rows again when opened; the end of a sign-in
forgets it (App's `watchUploads`), so nothing queued goes into the next one's folders. The panel goes
2.5 s after the last when none failed, else stays with Try again until closed, its title a live region. A folder that
can't be listed shows only ".." with its error. The tour's upload step rings Upload. Alerts asks the
unread count and lists that many (`alertsRead.ts`, at most 500) before it marks them read, so it marks
only what is on the page.

Dialogs: Settings' (first password, delete a user, reprocess), the picture's crop
(`PhotoCropDialog`) and Files' versions are `Modal`s (ConfirmDialog.tsx: focus in and back, Tab kept
inside - `focusTrap.ts`, shared with /shared's viewer -, Escape and Back are Cancel except while
busy; `label` names a card without a heading). Enter in the first-password dialog creates the user
only from its fields. The profile can't be edited or saved until it has loaded (Save sends the name,
the bio and the picture together); one that didn't come says why with Try again. Users ask the role
again while nothing answers, and their list and Load usage say when they failed. The photo viewer's
header is two rows below 720 px (Info, the name and Close; then the photo's actions from the right),
so Close and Delete stay on a phone's screen; a pinch and ctrl+wheel are native listeners that may
cancel (React's wheel and touchmove are passive), as the crop's wheel is; the stall watch is one ref
callback per video element (keyed by path) with a cleanup; a full size's Blob, and /shared's, carry
their type only when an `<img>` or `<video>` may show it from a `blob:` URL (`inlineTypes.ts`), else
`application/octet-stream`, so an SVG keeps its thumbnail and never runs as a page of the device's
origin; within 4 photos of the end of what is loaded the viewer asks for the next page (and after a
jump, within 4 of its top, the page above). A rating
comment left unsaved on a photo that left the collection goes with it (RatingPanel's `dropped`).
/shared: a download that stops is a line in the progress strip with Try again (the items still to
save) and a close, the grid staying; a download's piece with no answer, or the bridge's, is asked
again after 1, 3 and 6 s; the viewer takes the focus as it opens and gives it back to its tile. The
feed lets an autoplayed clip fetched whole go once its post is off the screen and fetches it again on
the way back. On this day's thumbnails have a generation of their own (another sign-in only), so a
refresh never drops the answers on their way. A collection without a name reads "Untitled
collection" everywhere it is named (`libraryStore.collectionName`).

"Your device was updated" (`components/DeviceUpdatedNotice.tsx`, rules in `deviceBuild.ts`,
`node web/scripts/device-build-test.mjs`): a single-page app never reloads itself, so a tab open
across a device update kept the old web app. A build is the web app's own entry script
(`entryScript`: Vite's hashed `assets/index-*.js` in the page's HTML): the page notes its own at
start, and after every accepted sign-in (`useWS.onSignedIn`: the first, and each a reconnect
makes) fetches `/` with `cache: "no-store"` and notes the one the device serves now; another one
shows a card at the foot of the page (in an always-present `role="status"` region, so it is
announced) with Reload (`location.reload()`) and an ×. Never reloads by itself; one notice per
build (dismissed, it comes back only for another build; back on the page's build it goes). Only
where the device served the page (not `localhost` dev, not `__OTC_CONFIG`); it works for every
account and needs nothing from the internet (an earlier draft compared `CheckUpdate`'s version,
which only the primary instance answers and which fetches the release list from GitHub).

The first-run tour on the web (tour 2, 2026-10-27: `components/Tour.tsx`, its drawings `TourArt.tsx`
+ `.css` and their timer `tourMotion.ts`, rules in `tourSteps.ts`, the seen flag in `tourSeen.ts`;
`node web/scripts/tour-test.mjs`, and `back-stack-test.mjs` for its history; the phones have the
same tour natively, plus a step for their own photo backup - see Native apps): a card in a native modal `<dialog>` that
teaches one skill a step, by showing it - welcome (the menu), search, dates (the scrubber), select,
viewer, people (only while face recognition is on: steps are kept by id, so People can come or go
mid-tour), collections, files (List/Grid/Storage, sorting), upload (drag in, Share, Download ZIP,
the lock and versions), social (like, comment), post, friends, users (2026-10-28, owner only: a
user for each person, their own Off The Cloud on the device - Settings › Users, Add User, the first
password, and the sentence on Request bridge access only on the bridge; Tour's `useOwner` asks
`GetInstanceRole` as the tour opens and keeps a reply for the page, the owner's `GetBridgeAccess`
too; the count waits for it), alerts (Alerts and Settings, and where to replay it) - with Skip on
every step. Each step shows its section (`cStepSection`: Tour
calls App's `tourShow`, a correction of the tour's own history entry, so Back is Skip; never
Alerts, which marks every alert read as it opens - the last step shows Settings), and ending it -
Skip, Done, Esc, Back, the dimmed layer - shows the section it opened over (`tourFrom`, noted at the
first step). Every step has a small moving drawing of its controls (`TourArt.tsx`: plain React and
CSS, labels from the real controls' own keys, colours from the theme's variables, gradient tiles
and outline faces for "photos" and "people", samples `web.tour.art.*` - `art.thing` stays English
because the device's tags are RAM++'s English words - a timer `useScene` steps through each scene's
states, one still state under reduced motion; `aria-hidden`, the text says it all), so the tour
teaches an empty library; every label a text quotes is an argument from its own key. Where a
step's real control is on screen (`cStepTargets`: the search field, the scrubber, the first photo -
its circle shown, `data-tour-lit` -, a face, a card, Files' view switch and a heading or Sort by, a
row's tick and the drop tip, a post's heart and comment field, `+ New post`, the address and the
friend's field, Settings › Users' add row and bridge box once its list is in, Settings' Show the
tour - `data-tour="show-tour"` - and the menu's Alerts and Settings) the dimmed layer - an SVG path, evenodd - leaves a hole with an ember ring around each
(`holesFor`, touching ones merged), found again on every change of the page behind (a
MutationObserver, once a frame) and scrolled to once a step. The card's text is never cut
(2026-10-28; `tourSteps.planCard`, from 600px): the card goes whole beside the holes (`cardBeside`,
the roomiest side) as the page is, else after a scroll that makes the room (the top bar's and the
menu's controls stay put, the rest stay in the band below the top bar - all of them when they fit,
else the first; the least scroll when they show already, else the nearest to `scrollToShow`'s
middle; a short page given room at its foot first), else in a corner that covers none of them; only
then with a smaller drawing (`data-art` compact, then none; `cardSizes` reads TourArt.css's
`--ta-full`/`--ta-compact`), and as a last resort whole where it covers least. While the tour's own
scroll runs the card already sits where it will be; after it, it is placed for where the page is.
Below 600px it is a sheet at the foot with the largest drawing that keeps its text whole, controls
it would cover left out before the holes merge (so one above it keeps its ring) and the page given
room at its foot (`html[data-tour-room]` padding) so a control can be scrolled up above it. A click in a hole ends the
tour where it is and goes on to the control under it (`elementFromPoint` once the dialog is closed;
App's `keepSection` makes the section an entry of its own over the one the tour opened on). It
opens by itself once per browser origin, the first time the signed-in shell shows, however the
sign-in came (a restored session too) - never over a new device's setup, in the phone apps' web view
(`__OTC_CONFIG`), on `/shared` or with `?download`: `claimAutoTour` writes
`localStorage.otc_tour_seen` = `cTourVersion` (2, so a browser that saw the first tour sees this one
once; raise it only for a rewritten tour) before it shows, storage that throws means no tour by
itself, and Sign out keeps the flag. Show the tour (account menu, between Settings and Sign out;
Settings › Getting started, the last section of Settings on the web and the phones) replays it from step 1 and leaves the flag alone. App
keeps `tour` (`{step, auto}`), so it comes back at its step after the unreachable screen, and drops
it when the session ends; it is a lazy part (`lazyPart`, in a layer's `Part`: none of it, its rules
or drawings is in the first chunk - `tourSeen.ts` is), and nothing else opens with it (notifications
are asked for from the feed's card). Its keydown listener stops every key (arrows step, Tab stays in
the card; the explicit `role="dialog"` is for Files' capture-phase Escape), wheel and one-finger
touch moves are cancelled unless the card's text scrolls, the focus goes back to the opener if it had
keyboard focus, else the account button, and a polite live region reads each new step. It sends
nothing that changes anything (only the reads its sections make, and its own `GetInstanceRole`/
`GetBridgeAccess`), so the demo needs nothing.

Tooltips on the web are `data-tip` (`components/TipLayer.tsx`, mounted once in appEntry.tsx: shown at
once on hover and on keyboard focus, in a portal), not `title`, whose tip waits about a second. An
icon-only control also gets an `aria-label` (`data-tip` isn't read out); a text button whose tip
explains more carries the same text as `aria-description`. `title` stays only for a cut-off name's
full text (Files' rows and tiles, pickers, names), a button that is `disabled` while its tip
matters (Files' Delete over an upload-only pick: some browsers give a disabled button no mouse
events), and the map iframe's name.

Screen mode (the web app on a touch screen plugged into the device: the device's kiosk browser,
cage + Chromium, opens `http://127.0.0.1:<port>/?screen=1`): `components/screenMode.ts`.
`?screen=1` turns it on and `?screen=0` off, kept for the tab in sessionStorage `otc_screen` (a
reload, a section's own address). `screenMode()` is read once at load. `screenPart.tsx`, in both
entries, installs the guards at import and lazy-loads `ScreenLayer.tsx` (its own chunk; a chunk
that doesn't come leaves the page as it is): the keyboard, the note and Back. In the kiosk a new
window covers the page for good and another site has no way back, so the guards (a capturing
click listener and `window.open`) let nothing open a window or leave:
- another origin (a mailto included) gets the note `web.shell.screen.elsewhere`;
- an `<a download>`, a blob/data URL or a device file outside the app's pages (`/`, `/shared`)
  gets `.download`;
- a file field's click gets `.upload`, and no chooser opens;
- `window.open` always returns null (every caller already handles a blocked popup); an app page
  opens in the same tab.

`screenNavigation` decides, tested in `web/scripts/screen-keyboard-test.mjs`. `DownloadButton`
isn't rendered and BridgePanel's provider sign-in gives the note. Back (`common.back`) shows on a
page other than `/` with history. The kiosk's own policies block downloads and file choosers too.

Signed in on the screen, it stays signed in (release 173, the screen key under "A screen on the
device"): in screen mode `useWS.refreshSessionToken` asks once for a key (`askScreenKey`:
`ReqIssueSessionToken.screen`) and keeps it with `pwCrypto.saveScreenKey` in place of the session
token (none is kept beside it; a key already held is never asked again, so the 30-minute refresh
does nothing); an answer with an expiry (a device from before) is kept as a session token, and an
error leaves the screen on session tokens. `loadStoredSignIn()` (the key in screen mode, else the
token) is what App's restore and a reconnect redeem; `authWithToken` sends `screen` for the key,
rotates nothing after it, and a refusal clears it at once (no wait for another tab's successor).
`passwordChanged` clears it, so the refresh after a change asks for another; Sign out clears it
(and the device ends it). Tested by `web/scripts/screen-key-test.mjs`.

The on-screen keyboard (`ScreenKeyboard.tsx`, layouts and rules in `screenKeyboardLayout.ts`,
typing in `screenKeyboardTyping.ts`):
- It shows while the focus is in a text field (`fieldFrom`: text/search/email/url/password/
  number/tel inputs, textarea, contenteditable; not read-only, disabled or inputmode=none),
  polled every 400 ms because a removed field sends no focusout. Its own key hides it (blur).
- Keys never take focus: pointerdown, mousedown and a native non-passive touchstart are
  cancelled. Keys act on release; Backspace acts on press and repeats.
- Typing is `execCommand('insertText'/'delete')`, so React's onChange, undo and selections work.
  Where that is refused, the prototype's value setter plus an InputEvent. Backspace and Enter
  dispatch their keydown/keyup first. Enter is then a new line in a multiline field, else
  `form.requestSubmit(submitter)`.
- Letters follow `t.lang`: QWERTY (en, nl, it; es + ñ, pt + ç), AZERTY (fr), QWERTZ + ü ö ä
  (de, 11 units). Long press (450 ms) gives the accents, the language's own first: slide to one,
  or let go and tap. Shift is one-shot; a double tap within 350 ms locks it.
- The 123 and =\< layers. Email and url get @ or / on the letters; number, tel and
  inputmode numeric/decimal get a number pad. The space bar shows the language's name, and
  Enter's icon and name follow enterkeyhint.
- Auto-capitals start a sentence only in multiline fields, or one-line fields that say
  autocapitalize sentences/words/on: most one-line fields here are names, codes and ports.
- Room: `body.osk-open` pads by `--osk-h`. A covered field is scrolled to the centre; in a fixed
  box (a dialog), that box gets `style.translate` up by the overlap (the `translate` property,
  so its own `transform` stays) until the keyboard goes.
- z-index 5000, over the dialogs (up to 2200) and under TipLayer.

Strings are `web.shell.keyboard.*` and `web.shell.screen.*`. Checked in muted headless Chrome
with CDP touch events, on a harness page and on the app at `?screen=1`.

Drag to select on the web (issue #200): Images, People and Files (list and grid) share
`components/useDragSelect.ts` (the gesture) and `dragSelect.ts` (the pure rules, tested by
`node web/scripts/drag-select-test.mjs`, the same rules as the phones' `DragSelect`). A mouse press
on an item that moves more than 8 px, or a touch held 450 ms then moved, adds every item from the
pressed one to the one under the pointer in display order after the selection it started from
(Images numbers them in grid order); moving back restores it, a press never unpicks, Files' `..`
is never picked, Escape takes the drag back, and the click it ends with is swallowed. Only a press
on an item itself starts one (`indexAt(el, true)`): a gap, the empty end of a short row, a month's
title, Files' header or `..`, or a face's name, count or "Add a name" (People's `faceAt`: the
picture and its circle only) start nothing, and the page scrolls. Each move
hit-tests with `elementFromPoint` (clamped into the grid and below `--topbar-h` - in Files below
the selection bar and column headings stuck under it, `stuckOver`/`stuckTop`, so the pointer over
them counts as above the window), and again on every scroll: the window scrolls in bands at the top
(from there too) and bottom (56 px, or a third of what can be
seen when that is less, none when that is under 8 px; up to 1200 px/s), but only once the pointer
has moved more than 8 px since the drag began (`DragPointer.armed`: at once for a mouse drag, for a
touch measured from where the long press fired - a long press held still in a band never
scrolls), and the wheel works mid-drag. The drag's two ends are kept by key (`relocate`, re-run
whenever the items change): when the pressed item goes the drag is over with the selection as it
stands - later moves do nothing and the release's click is still swallowed; when only the item
under the pointer goes, the selection stays as it is until the pointer is over another item; a
pick that went from the items goes from the drag's base too (`keptBase`), so the next move (or
Escape) doesn't bring it back. Something coming over the items (`enabled` false: a viewer, a
dialog, the date scrubber) or a new Files listing (a refresh, an upload finishing, another folder:
Files is disabled while it loads) ends the drag the same way (`interrupt`): the selection is what
the view makes it (the listing clears it), the edges stop scrolling, the release is no click and
Shift+click doesn't go on from it. The selection is committed with `flushSync` and the container's on-screen
move measured round it: Files' in-flow actions bar appearing at the first pick (or the browser's
scroll anchoring) must not move rows under the pointer, so the page scrolls by the difference,
adding a `min-height` to the document while something is picked when it can't. Text selection
and the picture callout are off only on the container during a touch press or a drag
(`data-dragsel`); the container keeps a non-passive `touchmove` listener (Chrome and Safari only
let a touch's moves be cancelled when one was in place as it began) that cancels only a touch
drag's moves, and the element a touch began on gets the same listener while it is held (its moves
keep going to it once a new listing or a face that went has taken it off the page). With touch, once something is picked a tap in Files picks or unpicks a row instead
of opening it. Shift+click (and Shift+Space on a checkbox) adds the run from the last pick or the
drag's end. The edge scroll waiting for a move, the band depth, starting only on an item, an
item going mid-drag and a new listing ending a drag follow the phones' rules: change all three together. Tested in headless
Chrome with CDP mouse and touch input against a mock `useWS`.

Leaving the selection on the web: Images' selection bar starts with an × (`web.images.select.clear`)
and Escape clears it; Files' actions bar (`.fb-actions`) starts with one too (`.fb-sel-clear`,
`web.files.selection.clear`, a 36 px circle in a 44 px target), and Escape clears Files' selection
unless the viewer, a pop-up, the menu's drawer, a drag (its own Escape takes the drag back), the
top bar or a text field has it. `clearSelection` unpicks everything, ends a drag under way
(`useDragSelect`'s `interrupt`) and moves the focus from the bar to the folder's contents; a tap
then opens again. Over a search's results the first Escape closes them and the next clears
the selection made in the folder underneath (its bar hidden with it), as Android's Back does.

Files' sticky strip on the web (web only: the phones have no column headings): scrolled down, the
column headings of List, a search's results and Storage (`.fb-head`, `position: sticky`) stay under
the top bar - `.fb-table` clips its rounded corners with `overflow: clip`, as `hidden` would stop
them sticking - and while something is picked the actions bar (`.fb-actions`, in the page just above
the list or grid, no longer in the toolbar) sticks between the two, alone in Grid. `FilesExplorer`
measures their heights onto `.fb-wrap` (`--fb-sel-h`, `--fb-head-h`) for the headings' `top` and the
rows' `scroll-margin` (keyboard focus; the viewer's return scrolls its row fully into sight). A
heading clicked while its list is scrolled down shows the new order from its first row
(`sortFromHeading`). Scrolled down or mid-drag, the bar coming or going doesn't move the rows (the
browser's scroll anchoring; mid-drag `useDragSelect`'s own measure); at the list's top a tick still
pushes them down by the bar's height, as before the bar stuck.

Full-size photos on the web (`components/fullSizeCache.ts`, `node web/scripts/full-size-cache-test.mjs`):
`MediaViewer` keeps every file it fetched whole (photos by `GetFile`, clips the device won't stream
in `ReadFile` pieces)
for the tab's session, so stepping back to a photo or reopening it shows full size at once with no
request - every step back and forth used to show the low-res tile while it fetched again. By content
hash (the answer's `File.hash`, else the item's; no hash, not kept), least recently used out first,
100 MB (a tenth of the apps' default 1 GB thumbnail cache, the phones' `FullPhotoCache` share), one
photo at most a quarter of it. Kept even when the viewer moved on before it came. Memory only:
sign-out reloads the page. Read-ahead: once the open photo shows at full size and the viewer stays
on it `cRapidStepMs`, the next and then the previous photo are fetched into it (never videos, nor
around one; a held arrow key reads nothing ahead); `fetchWhole` shares one request per path, so a
step onto a neighbour on its way waits for it. Only after the open one, as on the phones: started
with it, they slowed down the photo being looked at. On the demo: the next or previous photo 2-26 ms with no
badge, was 100-230 ms with it.

Copies on the web (issue #201, `components/photoCopies.ts`, `node web/scripts/photo-copies-test.mjs`,
the phones' `DeleteCopies` rules): Images' Delete (selection bar and viewer) asks about copies when
a photo's `File.copies` is above 1 - one photo: `web.images.copies.single_text`, "Delete only this
copy" / "Delete all {count} copies"; several: `several_text`, "only these" / "every copy"
(`ConfirmDialog`'s outlined `other` button) - else, and always with `copies` unset, as before.
Every `DelFile` sends the tile's (or a Files row's) `hash`, and `every_copy` only for a photo with
copies (`delFileFields`). A single refusal (`copy_upload_only`/`changed`, whatever the others did)
shows the device's words in `web.images.delete.failed_detail` ("Delete failed: {detail}", the
phones' `delete_failed`; `refusedText`, also in the viewer and Files' alerts); two or more for one
reason are counted (`web.images.delete.*`), and different reasons follow the phones' `keptText`: a
counted line for `changed`, one for `copy_upload_only`, then any other refusal in its words in the
wrapper, once each (`deleteNotice` lines, one toast line each). Refused photos stay picked;
`changed` reloads the grid from where its search began (`reloadGrid`: a jump at its month, both
cursors reset) and a page refreshes a picked photo's hash (`refreshPicked`). The viewer closes on
`changed` (toast, reload, as the phones); so does Files' viewer (an alert with the words, as
Files' selection says them, and the folder listed again).

Photo edit on the web (2026-11; `components/PhotoEditor.tsx`/`.css` is the editor, its own lazy
chunk; `photoEditMath.ts` the pure crop maths; `photoEdit.ts` the rules, requests and answers;
`node web/scripts/photo-edit-test.mjs` tests them). The device side is in the Photo edits bullet
under Architecture.
- **Where:** `MediaViewer` shows Edit and Revert to original when its caller passes `onEdited`:
  Images (with collections and Favourites), Files, Memories' See all, Places. The demo refuses
  both at the press (`demo.ts`).
- **The editor:** a frame (`role="group"`, `web.images.edit.frame`) with corner and edge handles,
  for mouse and touch (pointer events; a second finger is ignored). The arrow keys move it 1%
  (10% with Shift), and Escape puts back a drag or cancels. There are Rotate left/right, an Aspect
  select and Reset.
- **Save** uses `ConfirmDialog`: Replace (ember) or Save as copy (plain outline; `text` is
  optional, and the second button is red only for `tone="danger"`), then the copies question after
  GetEditState.
- **Refusals:** `refusalOf` decides. "changed" closes the editor and viewer through
  `onEditChanged` (Images reloads the grid as Delete does; Files lists the folder again; Memories
  and Places ask again and toast). Any other refusal stays in the question.
- **After a save:** the editor draws a small JPEG for the tile until the device's thumbnail comes.
  Images swaps the tile in place (`shownAfter`), adds a copy next to the photo in the whole-library
  date view, and reloads as the viewer closes after "Only here" of several copies (`leftOthers`).
  The viewer fetches the full size again when the hash changes at the same path.
- **Phone layout:** `--pg-hdr-h: 79px` sizes the viewer's picture under its two-row header below
  720px. It used to cover the actions row.
- **Videos** (2026-11-08; `videoEdit.ts` the rules, `VideoEditor.tsx` its own lazy chunk,
  `node web/scripts/video-edit-test.mjs`): Edit on a video the device can cut (`canCut`: the
  device's eight types, or their extensions for an empty or generic type) opens the composer's
  trimmer (`VideoTrimmer`'s `cut` prop: Save in place of Apply, no Remove trim, `video_cut_note`
  under the length, a changed range asks to discard) on the viewer's own stream or clip
  (`hiURL`), the viewer's video paused. Save asks as a photo's (`video_save_text`,
  `video_replace`, `video_copies_text`) and sends EditVideo (`cutOf`: an end within one 0.1 s
  step of the video's end goes as 0, the end); `reqEditVideo` is open-ended in `ws.ts`. After a
  Replace the `<video>` is keyed by path and hash, so the new content streams anew; the tile has
  no picture until the device's thumbnail comes. A video's Revert says `video_revert_text`.

Largest files and Compress on the web (2026-11-08; rules in `components/compress.ts`, `node
web/scripts/compress-test.mjs`, cases C1-C13 the dialog, J1-J6 the job, L1-L10 the list, R1-R2 the
viewer - the phones' `CompressTests.swift` and `CompressTest.kt` use the same ids; device side under
Photo edits' Compress). Settings › Largest files (after Background work, on any device) opens
`LargestFiles.tsx` (its own chunk) as a back-stack layer over Settings: `ListLargestFiles` 50 a page
with `usePageRetry`, a row per content (small thumbnail by path, else `FileTypeIcon`; name, folder,
size, "in N places"), `largest.needs_update` on `unknown_payload`; photos and videos open in
`MediaViewer` with `copies`, other files as Files opens them (`fileOpen.ts`, Files' helpers moved
there). A checkbox or a touch long press picks; the bar has Compress alone. Images' selection bar has
Compress before Delete (breakpoints re-measured in German: labels from 1024 px, 1240 in a
collection, 1600 among one person's photos, 1840 both). `CompressDialog.tsx` (its own chunk; the
demo refuses at the press) asks `GetCompressOptions` for the picked photos and videos only, each
path once, the first 500 (`compressItems`); a videos radio (1080p, 720p, HEVC, Leave as they are;
default 1080p, else HEVC, else 720p; an item not offered the choice falls back to HEVC), a switch per
photo preset, `compress.row` sums, the not-offered list with the device's reasons, the total, the
warning and the slow note. `CompressFiles` starts the job and clears the selection; `compressJob.ts`
follows it (`GetCompressJob` every 2 s while the dialog shows it, every 10 s while the page that
started it is open; `watchCompress` forgets it at the end of a sign-in), and each done result's File
replaces its tile or row; skipped counts as not compressed. `ViewerItem.originalRemoved` hides Revert
and Info shows `compress.compressed_note`.

Newer photos above a jump on the web (issue #194, `components/newerPhotos.ts`,
`node web/scripts/newer-photos-test.mjs`): after a scrubber jump (`cutoff`), `PhotoGallery` keeps an
upward cursor - `upRef` (token, end), `upMapRef` (its `have`; `mapRef` is the jump's search alone,
`dropDeleted` lowers whichever brought a photo), `upLoading`, its own `usePageRetry`. Nothing is
asked at the jump: the first scroll up near the top arms `.pg-up-mark`, however made (the phones'
rule): any scroll event that lowers `scrollY` by more than a pixel with the page's height unchanged
(the jump, photos put above and a new width change it) while the mark is within `cUpReach` (600px)
- the scrollbar dragged too (`scrollArmsUp`) - or, as the page can't scroll above its top, a wheel
up, Arrow Up/Page Up/Home/Shift+Space or a finger drawn down while it is (`nearTop`); a new search
(`searchGenRef`) drops what the old one saw. The mark then asks `upwardRequest` (`newer_than` = the
cutoff, never `before`) once within 600px of the screen. The viewer open within 4 photos of the top
asks it too (`viewerAsksUp`, no arming: opening one of them asks for what is newer), as near the end
it asks for the next page - a jump's first photo read as the newest, with no Previous.
`oldest_first` false drops the page and stops it while the page is open. A page goes above newest
first, without hashes shown (`notShown`), inside `keepTopTile`: the first tile below the bar
(`data-key`) is measured around a `flushSync` and the window scrolled by what it moved (Chrome's
anchoring leaves 0; Safari has none), and `openIdx`/`anchorRef`/`topTileRef` shift. The top month
(not the jump's) keeps its bucket's room, grey before its photos (`topReserve`, whole rows past 4
one `.pg-skel-rows` block); spinner and error float in `.pg-up`, a sticky 0-height pill rendered
only while the mark is within reach (`upNear`, its own observer), never pinned over the whole grid.

Files' Storage view on the web (issue #195, `components/folderUsage.ts`,
`node web/scripts/folder-usage-test.mjs`): the third `fb-viewmode` button, remembered in
`otc.files.viewMode`. Only there `loadUsage` asks `GetFolderUsage` - on switching to it and with
every listing while it shows (`loadList` checks `viewModeRef`) - dropping answers for another folder
(`usageAnswerFits`: the latest request's, for the folder shown). `unknown_payload`: `web.files.usage.needs_update`, not asked again
while the page is open; other errors the device's words; no answer `usage.failed` + Try again. Rows
are the answer's order (no Sort by), joined with the listing by (path, is_dir) for List's lock and
Images switch; a versions-only child is a plain folder row. Bar and share of files + versions
(`shareOf`: "under 1%", no bar at a 0 total; `row_label` on the bar), size, `..` on top; the total
and versions lines above, the note below; nothing is picked. Below 560px the bar has its own line.

Issue #182 (and #175/#176): accounts can be deleted - `DELETE /api/account/me` (`{"confirm":
"delete", "password"}`, or a sign-in within 15 minutes for Google/Apple-only accounts;
`dao.DeleteAccount` removes the account, its logins, tokens and app codes, releases its domains
with the usual 30-day hold and forgets their push rows and metrics) - and exported (`GET
/api/account/export`, JSON). A device gives its own name back with `BridgeReleaseDomain`
(authenticated by its secret, like `RotateBridgeSecret`). An unknown name is answered with
`error_code = "domain_not_registered"`; a primary device that keeps getting it for 15 minutes
(`websocket/bridge_leave.go`) leaves the bridge by itself, with an Alert. Leaving (that, or the
owner's "Leave the bridge" - `ReqDisableBridge` from the web BridgePanel and the apps' Settings >
Bridge and Account) sets the domain back to `otc` and writes `off <left|released>` to
`bridge.request`: the root runner empties `[otc] bridge-addr`, records `{"state":"off"}` (read
back as `RespBridgeAccess.left_reason`) and restarts the device local-only at
its home-network address; `RespBridgeAccess.local_address` (`localAddress()`: the source address of
the route out, with `[otc-api] port`) is what the apps switch their endpoint to (`ws://<it>/ws`),
since `.local` names don't resolve everywhere (Android) - `otc.local:8080` only when it is empty. The privacy
notice is `bridge/static/privacy.html` (`/privacy`, English in every language: legal text),
linked from the landing page's footer (`bridge/site/pages/landing.html`), the account page,
BridgePanel and the apps' sign-in and Settings; contact messages are pruned after a year.

Terms of use (issue #175): `bridge/static/terms.html` at `/terms` (operator "Off The Cloud",
info@off-the.cloud; free for two years then 9.99 € + VAT a year; the device, its data and its handling are
the owner's responsibility and Off The Cloud is not liable for data loss; Dutch law; English in
every language, like the privacy notice). It is linked from the landing page's footer, the
privacy notice, the account page and the setup wizard. Accounts
record `terms_version` + `terms_accepted_at` (migration 008; `accounts.TermsVersion`, bump it when
the text changes in substance). Email sign-ups accept with the checkbox. Google/Apple accounts
accept on the "complete your profile" page, where the OIDC callback sends them until they have
(`TermsAccepted`). Older accounts accept with `POST /api/account/accept-terms` from a prompt on
the account page. A setup code (`IssueSetupToken`: `ErrTermsNotAccepted`) and a hand-registered
name both need the terms in force.

Retention (issue #176):
- **Accounts unused for 6 months are removed.** `accounts/inactivity.go` runs daily on both nodes,
  and only when email is configured. "Unused" means no account-page sign-in (`last_seen`), no
  client reaching any of the account's devices (`devices.last_client_at`) and none of them
  connected to the bridge (`devices.last_online_at`, migration `012-last-online.sql`): a family
  using its device only at home or over Tailscale never has a client relayed, but the device stays
  connected. Each node stamps the devices connected to it (`websocket.Manager.onlineLoop`, never
  under a pool's lock; `dao/lastonline.go`): as the first connection to the node registers, every
  `cLastOnlineEvery` (10 min, checked each minute) while connected - one UPDATE for all due - and
  as the last one closes (at most once a minute), never moving a stamp back. On top, a device
  connected right now (`IsOnline`, any node) keeps its account from being warned or removed and
  forgets its warning (`inUseNow`), whatever the stamps say. Without the column (migration not
  run) nothing is stamped and every query falls back to the one without it (`lastOnlineCol`,
  like `accounts.lang`). The job:
  - warns by email a month before (`inactivity_warned_at`, migration 009, claimed with a
    conditional update so only one node sends it);
  - unmarks a warning whose email failed, to retry it the next day - unless RCPT TO was refused
    for good (5.1.1/5.1.2/5.1.3/5.1.6/5.1.10, `mailer.AddressRefused`), which counts as warned;
  - forgets the warning once the account is used again;
  - removes accounts like "Delete my account" would.

  `TestInactivityPassMySQL` runs against the Lima VM's MySQL (`OTC_TEST_MYSQL_DSN`).
- **Server logs** are kept 30 days: journald `MaxRetentionSec` on every node,
  `bridge/cluster/journald-otc.conf`.
- **Contact messages** are deleted after a year, or from the admin panel's Messages tab.

GDPR paperwork (issue #175) lives in `private/gdpr/` (the maintainer's copy only, never
committed): `records-of-processing.md` (Art. 30, every table, purpose, legal basis, retention,
processor) and `breach-procedure.md` (72 hours to the Autoriteit Persoonsgegevens, with a
register). The App Store privacy answers are in `private/stores/app-store-privacy.md`, matching
the iOS and macOS `PrivacyInfo.xcprivacy` (name, email, APNs token, sent logs; photos and files
aren't collected because the relay keeps none of them). Play's Data safety answers are in
`private/stores/play-store.md`. Change these with the privacy notice whenever the bridge stores
something new.

Email (`bridge/mailer`, `[smtp]` in the bridge's ini: host `smtp.protonmail.ch`, port 587,
username/from `info@off-the.cloud`, `password-file=/etc/otc/smtp-token` - a Proton SMTP token,
0600 for the service's user, pushed from the Mac's Keychain item `otc-bridge-smtp`, never in the
ini or the repo): verification is mandatory - an email sign-up gets a link (`#verify=` in the URL
fragment, 48 h, only its SHA-256 in `account_email_tokens`) and `IssueSetupToken`,
`AccountForSetupToken`, manual name registration and `/api/claim` with the account page's session
cookie (403 `cConfirmEmailFirst`, or `cAcceptTermsFirst` without the current terms) refuse an
unverified account (a database error loading the account there is a 500, not "confirm your email";
`LookupSetupToken` is the error-aware form of `AccountForSetupToken`, and `IssueSetupToken` returns
database errors as they are, so `ErrEmailNotVerified` means just that); Google/Apple
accounts are verified by the provider, and linking one verifies an email account - clearing the
password of one that was never verified (someone may have signed up in the owner's name) and
ending its sessions, while a verified account keeps its password and its address is told so
(`notifyProviderLinked`, `mail.provider_link.*`, greeting no one by name: whoever signed up may
have had their confirmation link clicked, and "Forgot your password?" ends their sessions); accounts from
before migration 007 were kept verified. `?for=setup` sign-in/sign-up of an unverified account
answers `verify_email: true` (and resends the link on a sign-in); the setup wizard shows "Confirm
your email" and signs in again. Password reset: `/api/account/forgot` (same answer for any
email) mails a one-hour `#reset=` link; `/api/account/reset` sets the password, verifies the
email and ends every other session. A link is spent only in the transaction that stores what it
is for (`ResetPasswordWithToken`, which takes the account's other reset links with it, and
`VerifyEmailWithToken`): a write that fails leaves it working for the retry, and a reset looks
at its link before the bcrypt. New passwords are 8 to 72 bytes (`cMaxPasswordBytes`, all bcrypt
keeps - x/crypto refuses to hash more): longer is `password_too_long`, at sign-up, on a change
and on a reset; a sign-in still takes any length. A new link is sent before older ones are dropped
(`KeepNewestEmailToken`), and a QUIT error after an accepted DATA counts as sent.

Issue #163 (request limits, `bridge/limits`): every JSON body goes through `limits.DecodeJSON`,
and the sign-in callback's form post (Apple's) through `limits.ParseForm`
(64 KB, 15 s to arrive - the servers bound only headers, since a whole-request `ReadTimeout`
would also cut the websockets the cluster router proxies); one-off device GETs (static assets,
`/media`) are limited per address (`oneOffPerAddr`, 10/s, burst 60 - the forwarded address for a
cluster hop; before the device's disabled flag is looked up) and per device to `cOneOffConcurrent` (3) in flight, so they can't drain a device's
pool. A `/media` response counts once however many spans it takes, and fetches them through a
`websocket.Session` (`session.go`, `api/media.go`): one of the device's connections for the whole
response - a pool connection per 4 MiB span had sat idle and started each with a cold TCP window,
the device dialling a replacement for each - with the next 2 spans asked for while one is written
(`cMediaReadAhead`; the device answers a connection that hasn't signed in 4 requests at a time, each
span holding twice its size of its memory). Spans asked ahead are held, until written or dropped,
under the node's `mediaAhead` budget (512 MiB, 32 MiB per address - an IPv6 /64), asked only while
it has room: a response always has one span of its own outside it, as before it asked ahead, so a
client that stops reading pins one span plus what fits, not three. Each span still holds one of the device's 3 one-off
slots while in flight, in turn with the static assets'; at most 4 connections per device are kept
for sessions (`cMaxSessionConns`, past them a response goes the one-off way) and one with nothing
asked of it for 10 s (a paused player) is given back (`cSessionIdle`). Kept: a range answered
whole (AVFoundation), HEAD from one byte, suffix ranges, nothing more asked once the client hangs
up; `/api/device-online` answers 1 a second per address after 10 (429 with Retry-After: it may ask
Redis), and the cluster router asks Redis which node holds an address only when it can be a
device's (`websocket.IsDeviceHost`); `auth_events` takes one row per address and reason a minute; relays read only an
envelope's id (`envelopeID`, protowire) instead of unmarshalling every frame; store errors reach
clients as `cInternalErrorMsg`; sign-ups are limited to 5 an hour per address, sign-in answers
the same for unknown, wrong and Google/Apple-only accounts, and new bridge passwords use bcrypt
cost 12 (`limits.BcryptCost`; account hashes are upgraded at the next sign-in).
`limits.Rate` deliberately has **no hard cap** on its map (a0ce30c removed one): refusing unknown
keys would let a flood of distinct addresses (one IPv6 /48 is enough) lock out everyone new, for an
hour on the sign-up and email limiters. Its size stays bounded by request rate x refill window, and
besides the time-based sweep it sweeps once the map has doubled since the last sweep, with a 100k
floor (`cSweepKeys`). The account sign-in limiter's failures map is the same: no cap, stale
addresses swept as it doubles (10k floor), never a live lockout dropped - each failure costs a
bcrypt; and a sign-in that succeeds clears nothing (failures age out of the window), or a sign-in
to one's own account between guesses at another's would reset the count (the admin panel's still
clears: nobody can make an admin). Every per-address throttle (both sign-in limiters, sign-ups,
emails, sign-in starts, the
contact and claim cooldowns, lookups, one-off GETs, setup beacons, pairing) counts
`clientaddr.Bucket`: an IPv4 address, or an IPv6 /64, which one host can rotate through; logs,
`BridgeClientInfo` and stored rows keep the address itself. Since a /48 (free from tunnel brokers)
is 65,536 such buckets, sign-ups,
failed passwords, the "send me an email" requests, the contact form, Google/Apple sign-in starts and
setup beacons (the last two a database write each) are also bounded per network
(`clientaddr.NetBucket`: an IPv4 /24, an IPv6 /48 - 20 sign-ups an hour, 40 failed passwords in 10
minutes, 30 emails an hour, 5 messages a minute, 120 sign-in starts a minute, beacons 20/s with a
burst of 100) and on the whole node (120 sign-ups an hour; 120
emails an hour that strangers can have sent, `emailBudget` - the new-account note to the project
goes after the person's confirmation, and only while 10 more stay (`AllowLeaving`, `cNoteReserve`),
so it is the first to go; 30 contact messages a minute); and at most half the cores hash passwords at once
(`limits.HashPassword`/`CheckPassword`, the admin panel's too): a hash that can't start within 3 s
is a 503 with Retry-After and the handler's own "could not ... right now" code, never a failed
password. Every request that hashes a password it carries takes a token per address and per network
before any work, the right passwords too - failures are counted only once their bcrypt is done, so
many sent at once all got one: an account sign-in, reset, password change or owner confirmation
(`passwordTryAllowed`: 10 a minute an address, 60 a network; a 429 `too_many_attempts` with
Retry-After), and the admin login and password change (`attemptAllowed`: 10 and 30 a minute), whose
failures also count per network (20 in 15 minutes lock the network out, `netLimiter`). The
unauthenticated requests that ask the database whether what they carry is real are
bounded per address and per network before they ask: name-available, claims (whose setup code is
looked up first) and setup-lookup in `bridge/api` (`allowLookup`: 5/s, burst 40 an address, 20/s,
burst 150 a network - the wizard checks a name as it is typed and polls the hand-off every 2 s), and
setup-token-info, verify, reset, app-exchange and the sign-in callback in `bridge/accounts`, which
share one (`lookupAllowed`: 2/s, burst 20 an address, 10/s, burst 100 a network); past either, a 429
with Retry-After. An account makes at most 10 setup codes in 15 minutes on a node
(`codesPerAccount`; past it a 429 with Retry-After and the code's own "could not" text). An
account email (confirmation, reset, new identity, inactivity warning) greets the account by name
only once its address is proven (`greetingName`; the warning reads `dao.InactiveAccount.EmailVerified`):
whoever signs up types the name and picks the address. A Google or Apple sign-in that proves the
address of a password account nobody confirmed takes the account over and drops what that
sign-up typed or accepted (`dao.ClearUnprovenAccount`, one statement: the password, the country
and the terms go, and the name and surname become the provider's, or none), so no later email
greets the owner by a name a stranger typed; the owner lands on the profile page to complete it.
The contact and claim cooldowns, and the admin login's limiter, are swept on a timer (or as the
map doubles), not at every request. The account sign-in's, sign-up's and email 429s carry Retry-After,
as the admin login's does. Proxied device responses go out through `limits.WriteAll`
(deadline renewed
per 32 KiB: 30 s for static assets, `cMediaWriteStall` 5 min for media, since paused players stop
reading) and the cluster router's `deadlineWriter` - every forwarded request (a stray `Upgrade` header
alone is an ordinary one: `isUpgrade` is ReverseProxy's own test), and a forwarded websocket's
hijacked connection too (`stallConn`, 60 s like `writeClient`); no `http.Server` `WriteTimeout`, because of the
websockets. Only hashed Vite assets (`/assets/<name>-<8 chars>.<ext>`) are cached `immutable`. Only
`dao.IsDuplicateKey` (1062) means 409 "taken"; other insert errors are a 500, which releases the
contact/claim cooldown (`releaseCooldown`).

### Bridge accounts (`bridge/accounts`, issue #124)

A database error in a sign-in or setup check is "try again" (500), never a refusal (issue #189):
`session` / `AccountFromRequest` return it (`RequireAuth` answers 500, a claim with a session
too; `Me` answers 500 keeping the cookie, which it clears only for an account that is gone; the
admin panel's `RequireAuth` 401s only an admin that is gone), `LookupSetupToken` tells "no such
code" from an error (claims with a setup code, and
name-available's `yours`/`online`), and `accountAddDomain` reads the account once. The setup
wizard passes the bridge's own error answers through (`bridge_answer`; `bridge_error` keeps their
`error`, `code` and `args`, and the page words known codes as `wiz.bridge.<code>`, each key's
English the bridge's own: `TestWizardBridgeTextsAreTheBridges`), and a typed
code is only
"not valid or has expired" when the bridge said so (a 5xx is a 502 "try again").

Every domain registered on the bridge belongs to an account (`devices.account_id`). The package
owns identity and sessions: email+password sign-up/sign-in (bcrypt, per-address throttling),
Google and Apple as plain OpenID Connect authorization-code flows in `oidc.go` (no SDK: the
provider's JWKS is fetched and cached - one fetch per provider at a time, outside the lock, and
while refetches fail the last good keys verify for up to 24 h - the id_token verified with
`golang-jwt/jwt/v5`; Apple's
client secret is an ES256 JWT signed with the team's .p8), HMAC-signed session cookies like the
admin panel's (`otc_account`, path `/`, same signing secret), and **setup tokens** - 8-character
codes from an unambiguous alphabet, 15 minutes, kept in `account_setup_codes` by their HMAC
under a key derived from the session secret (never as they are), with whether the code was made
right after its owner signed in - which are the only link
between a wizard's claim and an account. A device's relay secret is kept only as its SHA-256
(`devices.secret_hash`, `dao/secrets.go`); an existing bridge gets there in two steps around a
deploy, migrations 013 and 014, with both forms written in between for a node still on an older
release (`bridge/cluster/README.md`, "Hashed secrets"). The domains themselves are handled in `bridge/api`
next to the claim (`accountDomains`, `accountAddDomain`, `accountNewIdentity`,
`accountReleaseDomain`), since they share the name rules: `claimName` takes the token from the
body or `Authorization: Bearer`, refuses with `401 login_required` when there is none (unless
`[accounts] open-registration=true`), hands a name the same account already owns to the new
identity (`dao.ReplaceDeviceIdentity` - how a lost device is replaced), and caps an account at
`accounts.MaxDomains` (403 `domain_limit`). Moving a name to a new identity relays every client
of it there, so a session cookie alone (a borrowed laptop, a stolen cookie) never does it: a claim
for the account's own name needs a setup code made right after its owner proved who they are
(a password or a Google/Apple sign-in just made - `?for=setup` sign-ins, the provider flows, the
app's exchange - or the account page asked within 15 minutes of a sign-in, whose answer then
says `owner_confirmed`) or a session that is a sign-in that recent (`FreshSignIn`), else 403
`move_needs_signin`; the account page's New identity (`accountNewIdentity`) needs `ConfirmOwner`
(the password, asked inline, or a provider sign-in within 15 minutes); and either emails the owner
(`NotifyNewIdentity`, `mail.identity.*`). A code made for a session alone still registers new
names. A name the account released less than 30 days ago (`dao.NameHold`: held for it, where
`NameHeld` is held from everyone else) is still its own here - else a session would release it and
register it again for a device of its own: a claim takes it back only as it moves one, the account
page's add (`accountAddDomain`) asks `ConfirmOwner` for it (the page shows the password field after
the `wrong_password` that no password gets - answered at once, unhashed and uncounted), and both
email the owner; a hold that can't be read is a 500. Release (`accountReleaseDomain`) needs
`ConfirmOwner` as account deletion does: it takes the device off the bridge. `websocket.go`'s `ReqBridgeRegister` no longer
registers an unknown domain on dial-in unless open registration is on. A device's additional
users with bridge access (issue #103, `pepe.off-the.cloud`) get theirs from the primary:
`ReqRegisterUserDomain` (141, `bridge/websocket/user_domains.go`), authenticated as the primary
and carrying the user's own identity, registers a free name under the primary's account as that
primary's user's address (`devices.primary_domain`, migration `015-user-addresses.sql`,
`bridge/dao/user_addresses.go`; it counts toward `accounts.MaxDomains` - the owner's choice,
2026-10-23) and refuses another account's name or one held from it. It moves no name its owner
hasn't confirmed: any device of the account can send it, and a session alone puts a device on the
account. Only the asking primary's own users' addresses go to a new identity (the user added again)
or come back while held after their release (`released_domains.primary_domain`); any other name of
the account is `name_taken`, or `name_held` while held, and moves only with claimName's
`ownerConfirmed` or the account page's `ConfirmOwner`. Asked for with the address's own identity
(every start of the primary), an address registered before 015 is recorded as that primary's; a
name moved by `ReplaceDeviceIdentity` (setup, New identity) stops being a user's address, and so do
a released primary's users'. A move here emails the owner as well (`SetIdentityNotice`, set to
`NotifyNewIdentity` in main); without 015's columns nothing moves this way. A primary whose domain
predates accounts registers its users' addresses without one. The device asks in
`createUser` before provisioning (`websocket/user_domains.go`; a later failure gives it back),
gives it back in `deleteUser` (`BridgeReleaseDomain` with the user's identity), and at every
start of the primary asks again for every active user with bridge access
(`registerUserDomainsAtStart`, retried at 1, 5, 15 and 60 minutes): users created between #124
and release 127 were turned away until then. The account page is
`bridge/site/pages/account.html`: embedded in the binary, served by `bridge/api/site.go` at
`/account` and `/<code>/account` under the nonce CSP, plain JS over `/api/account/*` whose texts
come as JSON blocks (`site.account.*`, and `api.error.*` for its error table by code) built by the
`jstexts`/`jsform` template functions (`bridge/site/site.go`), filled in as text nodes only
(docs/i18n.md "bridge"). A 429 with Retry-After reads as its wait ("Too many attempts. Try again in
4 minutes.": `site.account.wait.*`, a `series` 0-60); a dead verify link offers "Send me a new link"
signed in (resend-verification), a dead reset link the email field for one (forgot); and
`profile_incomplete` (sign-up, profile) carries `fields`, a per-field code each (`name_required`,
`surname_invalid`, `country_required`...: `profileFieldCodes`), which the page marks under each
field, its `code` and English unchanged for older pages and the wizard. Its links (the emails'
`#verify=`/`#reset=`, the sign-ins' `?error=` and `?complete=1`, BridgePanel's `?delete=1`) all
land on `/account`, except that the emails
(verification, reset and the inactivity warning) link `/<code>/account` when the account's
`accounts.lang` is a carried language other than English (`accountPage`/`pageLink` in
`bridge/accounts/emails.go`; English, `""` and anything else keep `/account`, byte for byte). The wizard's account step
goes through the device (`/api/account` in `setup_wizard.py` proxies sign-in/sign-up with
`?for=setup`, which returns a setup token; a typed code is checked with
`/api/account/setup-token-info`) because the hotspot's captive DNS only lets the device reach
the bridge - so Google/Apple users get a setup code from the account page on another device.
Inside the apps' Bluetooth setup the phone has its own internet, so the
account step also offers "Continue with Apple/Google" (the wizard's `/api/providers`): the app
runs `/account/auth/<p>/start?return=otcsetup://done&challenge=…` in the system sign-in sheet
(`ASWebAuthenticationSession` / a Custom Tab + `SetupSignInCallbackActivity`) and, PKCE-style,
once the person says Continue on the bridge's question page (below), the redirect carries only a
one-time code that `POST /api/account/app-exchange` trades with the
app's verifier for a setup token (`bridge/accounts/appsignin.go`) - kept in the database (`app_signin_codes`, by SHA-256, single use), never one node's memory: the callback and the exchange can reach different bridge nodes (#144) - a custom scheme can be
claimed by any Android app. "Continue without an account" sets `skip_bridge`, and `install.sh` gets `OTC_BRIDGE_ADDR=""`
(local-only, name `otc`). Schema: `bridge/db/db.sql` + `bridge/db/migrations/001-accounts.sql`
for an existing bridge (no updater on the bridge: run it by hand, it is idempotent). Issue #139: the admin
panel's Devices tab shows each device's owner, whether it is dialled in (`Admin.IsOnline`, set
from the websocket manager in main), when it was last connected ("Last online", "online now" while
it is; `devices.last_online_at`, migration 012), when a client last reached it through the bridge
("Last used away from home"; `devices.last_client_at`,
written from Go in UTC at most once a minute per domain by `RecordDeviceActivity`; migration
`002-last-client.sql`) and relayed traffic over 1 h / 24 h / 30 days from `device_metrics`; an
Accounts tab lists accounts and opens one with its devices (`/admin/api/accounts[/{id}]`). Test bed: the
Lima VM `otc` has MariaDB with `bridge/db/db.sql` and the migrations loaded, and a test bridge
in `~/btest` (not `/tmp`, which the VM loses on every restart): `etc/otc_test.ini` with
`tld=bridge.test:8081` and `static=$HOME/btest/static/` (the trailing slash matters), a
self-signed cert, and `run.sh` to (re)start it; copy a linux/arm64 build of `otc_bridge` and
`bridge/static` in, the admin login is `admin` / `test-admin-pw`. curl with `-H "Host:
bridge.test:8081"` (Lima forwards the port); Chrome needs `--host-resolver-rules="MAP
bridge.test 127.0.0.1"`. Issue #143: every admin list (`/admin/api/devices`, `accounts`,
`auth-events`, `contact-requests`) takes `q`, `page`, `size` (25 by default, 200 at most) and
answers `{Items, Total, Page, Size}` (+ `Unread` for messages); searches escape LIKE's
wildcards (`likeArg`), and `/admin/api/domains` feeds the device pickers.

Sessions and sign-in: only a real sign-in (sign-up, login, provider callback, reset, password
change) issues the session cookie, whose issue time the 15-minute `cFreshSignIn` checks trust. A
password change or reset is one UPDATE that also bumps `session_epoch`
(`dao.SetAccountPasswordEndingSessions`, `LAST_INSERT_ID(session_epoch+1)`). A Google/Apple
sign-in's state is tied to the browser by `__Host-otc_oauth_<state[:16]>` (SameSite=None) plus a
`_l` twin without SameSite (Safari 12); a 48-hex state from an older build (`isPreCookieState`)
passes without. `GET /account/auth/{provider}/start` answers 400 unless it is a top-level
navigation: open it as a page, never fetch or frame it. `GET /api/account/continue` needs
`Sec-Fetch-Site: same-origin` (or an https Referer on the bridge's host). Audit F4
(`setupconfirm.go`): a sign-in that comes back to a device's page on the home network (BridgePanel)
or to an app (`otcsetup://`) never redirects there with the token or the app's code straight away:
the callback answers with the question page itself ("Continue setting up the device at <host>?", or
"Continue in the Off The Cloud app?" - `site.Options.SetupApp`; `site.ContinueSetup`, site CSP,
`X-Frame-Options: DENY`, `no-store`), or `/api/account/continue` does after the profile, and only
its Continue (`GET /account/auth/continue?id=`: a same-origin page navigation, not a prefetch,
signed in to the grant's account) mints the token or the app's one-time code. The app's PKCE
verifier alone doesn't cover it: anyone can start a sign-in in someone else's browser with a
challenge of their own (a link to the start, or to the account page with that return), and any
Android app can claim `otcsetup://`; the apps in the stores need no change (their sheet shows the
question, Continue's redirect reaches them as before). The grant is the callback's signed
`__Host-otc_setup_<id>` cookie (account, address, 15 min, used once), so a link to
`/account?complete=1&return=<another address>` leads nowhere; Cancel opens the account page.
`pageError` answers a browser with the site's `signin.html` in its Accept-Language (the
`api.page.*` text under a heading, a link to the account page), and plain text to anything that
doesn't ask for HTML; the starts' 429 carries Retry-After. The bridge DSN has
`timeout=5s` and read/write timeouts of 60 s (above InnoDB's 50 s lock wait); relay-path queries run under a 5 s
context (`cRelayDBTimeout`) - follow that for anything new there. A `device_metrics` write that
timed out is dropped, not retried.

### Desktop sync client (`app/desktop`, issues #119 and #120)

`otc-sync` is the Windows and Linux counterpart of the macOS menu bar app, written in Go inside
this module (it shares `proto/generated`), pure Go so `make desktop` cross-compiles all four
binaries into `dist/` from any machine (version `dev`: development builds never update
themselves). Publishing is `scripts/desktop-release.sh [--patch] "notes"` (or `NOTES=… make
desktop-publish`), like a device release from a clean, pushed `main` only (nothing uncommitted,
HEAD = origin/main, the version commit pushed to main): it builds from a `git archive` of HEAD -
never this checkout's other files - with the Go protobuf code generated there by the
`protoc-gen-go` go.mod pins (as devices do; `brew install protobuf` for protoc). It bumps
`app/desktop/VERSION`, builds the four binaries with it, writes
`desktop.json` (version, notes, per `GOOS-GOARCH` the download URL and SHA-256), signs it with the
release key (`scripts/sign-file.sh`, the same key and Keychain passphrase as `release.sh`) into
`desktop.json.sig`, uploads everything to the rolling GitHub release `desktop` (binaries first,
manifest last), checks what was published and commits the version. The website's Downloads section
(`/#download`, in `bridge/site/pages/landing.html`; it highlights the visitor's OS) links to
those stable URLs, through `/get/<target>` (the Growth tab's download clicks, above).
Self-update (`internal/selfupdate`): the tray checks 20 s after start and every 6 h, verifies the
manifest with the embedded release key, and shows "⬆ Update otc-sync to X"; a click downloads the
platform's file next to the executable, checks its SHA-256 and swaps it in (Windows renames the
running exe to `.old`, removed at the next start), restarts the systemd user service if it runs,
and relaunches. `otc-sync update` does the same from the command line. Linux installs go to
`~/.local/bin` so the user can replace the binary; a root-owned one asks for `sudo otc-sync update`.
The Mac app is distributed only through the Mac App Store (which updates it); its card on the
Downloads section links to the listing (`apps.apple.com/app/id6814658734`, live since 2026-10-06). The Android card links to Google Play (`play.google.com/store/apps/details?id=cloud.offthe.otc`, live since 2026-10-10); the iPhone card links to the same App Store listing as the Mac (one app record for both, live on iPhone since 2026-10-21).
**Connect / Disconnect.** Both desktop apps show the device and password fields only while
nothing is configured. That form is open from the start: the Mac's inline Settings, and the tray's
dialog at launch. Once connected they show the device and a Disconnect button: Mac Settings >
"Disconnect…" (plus Retry while not connected), the tray's "Disconnect from <device>…", and
`otc-sync disconnect [--yes]`. Disconnecting asks first, then removes every folder from the app and
clears the address and password (`SyncModel.disconnect` / config with no folders and no domain).
The files stay where they are. This is because folders kept across a change of device would sync
with, or delete on, the other device. The Mac wizard's "Sync this Mac with my device" asks the same
when another device is set (`SetupWizardView.switchDevice`), and so do the tray's Connect dialog
and the Mac's connect form when another device is entered while folders are synced - also with the
old password gone - then disconnect first; `otc-sync settings` refuses it and points to `otc-sync
disconnect`. A respelling of the same address is no change (`engine.SameDevice` /
`SyncPaths.sameDevice`). Another device never gets the password kept for the old one, which would
reach it sealed to its own key: the Mac's connect form needs a password and applies the address
and the password together (`SettingsStore.apply`, which stores them in `saveConnection`'s order
below; `SyncModel.bind` reconnects once, with the pair, after a 0.3 s debounce), and otc-sync's
Connect asks again for an empty password field unless
the address is the device the kept password belongs to (`passwordMissing`,
`desk.main.password_required`), and saves the old password's removal, the address, then the new
password, in that order (`saveConnection`; the same device: the password, then the respelled
address), so neither device is sent the other's; `otc-sync settings` for another device forgets
the old password before it saves the address and saves a new one after it (then writes config.json
again, so a running sync reads a password kept in the keyring), and reads it with golang.org/x/term
(echo off in a Windows console too); `config.Reset` forgets it as well. "Change Password…" (the tray, Mac Settings' ⋯ menu, `otc-sync settings
--password-prompt`) keeps every folder; "Wrong password" in the tray's status line and the popover
leads to it. The tray's Connect and Change Password wait up to 30 s and say Connected, turned down
(Try Again) or unreachable (`waitForConnection`). **A device without its storage** (issue #207):
`storage_unavailable` on GetPubKey, Auth or a signed-in answer is a `StorageError` /
`OnStorageUnavailable` (`onStorageUnavailable` on the Mac), never a wrong password or unreachable:
the socket closes and the device is asked again after exactly 30 s (`storageRetry`), at once on a
network change or a wake; the password, config, folders and records stay, and the folders wait as
for an offline device. otc-sync's status code is `StatusNoStorage` ("Device storage unavailable",
worded `desk.main.status_no_storage`) with the device's words in state.json's `status_message`
(the tray's tooltip and a click on the status line, the Connect dialog, `otc-sync status`; else
`desk.main.no_storage`); the Mac's `ConnectionStatus.noStorage(message:)`, an orange dot, the
words as a footnote under the status row. Tests: `wsclient`/`engine` `storage_test.go`,
`WSClientStorageTests` (a scratch SwiftPM package, as `WSClientLangTests`). A ws:// address for a device not on the home
network (`config.Unencrypted` / `SyncPaths.isUnencryptedRemote`: not loopback, private,
link-local, `.local`/`.lan`/`.home.arpa`/`.internal` or single-label) asks before it is used; the
CLI wants `--allow-unencrypted`. A typed address is put right first (`config.NormalizeAddress` /
`SyncPaths.normalizedAddress`, one table of cases in both tests; 2026-11-05, a Docker device's user
typed `http://mint:8080` and `10.2.1.171:8080` and neither connected): `http(s)://` becomes
`ws(s)://`, a missing or bare `/` path `/ws`, and a home-network host with a port other than
443/8443 and no scheme `ws://`; anything that could connect before stays byte for byte (a host
with no scheme or port, a public host with a port, a ws/wss URL with its path), so saved
addresses and sync records don't change. Saving, `Configure` / `ws.configure`, `deviceDomainKey`,
`Unencrypted` and the web app's address all go through it. In the tray a typed address that
hasn't connected after 30 s gets `desk.main.connect_still_trying_address` (with the example the
Windows prompt has no room for), and each new reason a dial failed is logged once
(`logDialFailure`). Removing a folder asks first ("its files stay"). A folder is
synced once: one equal to, inside or holding a synced folder, here or on the device (ignoring case
on macOS and Windows), is refused with why (`config.FolderOverlap` / `SyncModel.admit`); on the
Mac, a folder waiting for its disk picked again is linked to it again, not added twice. "Set Up a New Device…" is a small
button (Disconnect's size) at the right of "Start at login" in the Mac's Settings (the Language
picker is the row below, last, at the left), and its own section above Quit in the tray.
**Web app (issue #193).** Once a device is configured both offer "Open Web App": the Mac in the
popover's header beside the gear (where the header doesn't fit, its heading drops "— Sync" before
the button keeps only its globe), the tray as the last line of its top group, and `otc-sync open`
(prints the address, and opens it where there is a display). It is the configured address's host
and port at `/` - https for a bridge name or a wss address, http for ws (`SettingsStore.webAppURL`
/ `config.WebURL`) - opened in the default browser (`NSWorkspace.open`; otc-sync's
`internal/browser`, http/https only: ShellExecute on Windows, xdg-open on Linux). Never the
password or a token (the web app asks for the password itself), and never the home-network route,
whose certificate a browser can't pin.
**Start at login, only with consent.** The Mac App Store rejected macOS 2.0 (17) under guideline
2.4.5(iii): 1.0 registered itself as a login item the first time it ran and re-registered at every
launch. Now neither client registers anything until the user says yes. The Mac's
`LoginItemSettings` (`LoginItem.swift`, the helper below behind `LoginItemService`, faked in
`LoginItemTests`) records the answer as `startAtLoginChoice` (absent = never asked). Only a true
lets `launch()` re-register an item the system dropped, and never one the user switched off in
System Settings (`requiresApproval`: left alone, the checkbox shows it off, Settings says so with
"Open Login Items…", ticking the box opens Login Items instead of registering). 1.0's
`startAtLogin` key was written by the app itself, so it isn't consent: true means unregister
whatever is there (the key stays until that works) and ask; false was the user unticking it, kept
as a no. What a yes registers on the Mac, since 2.6, is the helper (`OffTheCloudHelper/main.swift`, its
own sandboxed target embedded in `Contents/Library/LoginItems`, bundle id
`cloud.off-the.OffTheCloud.Helper`): a launchd agent (`LaunchAgents/
cloud.off-the.OffTheCloud.Helper.plist`, `SMAppService.agent`; RunAtLoad, KeepAlive) that starts
the app at login and opens it again whenever it finds it gone without the owner's Quit. An App
Store update quits the app and reopens only an app that had a window on screen, so the menu bar
app stayed closed after every update until its owner noticed ("Skipping relaunch — app was not
visible before termination", 2.4 and 2.5); the update leaves a helper inside the bundle running
(Apple DTS, forums thread 763268). The helper looks every 10 s and opens the app (from its own
path's bundle, never by bundle id - a Debug build elsewhere shares it) once it is gone at two looks
in a row, at most 5 times an hour, backing off a failed open; then it exits, so the helper launchd
starts next is the bundle's current version. The app tells it by distributed notification
(`HelperSignal`): `<bundle id>.quitByOwner` from the popover's Quit (and its ⌘Q) only - an update
or a logout quits the app too - and `<bundle id>.launched` at every launch; launchd's SIGTERM at
logout (or the agent unregistered) forgets a Quit, so the next login opens the app.
`HelperLoginItem` is the real `LoginItemService`: its state is the helper's once registered, else
the app's own (`SMAppService.mainApp`, 2.5's registration, or the fallback when the helper won't
register); `launch()` moves an allowed 2.5 registration to the helper (`moveToHelper`), and
unregistering removes both. Tested on 2026-11-05 with a throwaway copy (bundle id
`cloud.off-the.relaunchtest`, signed with the development certificate, in `~/Applications`, then
unregistered and deleted): killed, it was opened again in 20 s; quit as the owner, it stayed
closed; its bundle swapped for another version while killed (as an update does), the new version
was opened in 16 s and the helper restarted from it; a SIGTERM to the helper after a Quit opened it
again. otc-sync relaunches itself after its own update (`selfupdate`), so it needs no helper; it
isn't opened again after a crash unless it runs as the systemd service. The question is asked once, inline (never an `.alert`, which closes the popover):
`LoginItemOffer`, "Start Off The Cloud when you log in?" / "Start at Login" / "Not Now", shown
below the folder list and above Add Folder/Quit once a device has connected this launch
(`SyncModel`'s onConnect calls `deviceConnected()`), hidden while Settings is open, and also on
the setup wizard's "Your device is ready" page after "Sync this Mac with my device". Either answer,
or Settings' "Start at login" checkbox (`StartAtLoginToggle`, the real `SMAppService` state,
re-read when the popover opens), records the choice. otc-sync does the same with config.json's
`autostart` (nil = never asked; `autostart.AtLaunch` restores a consented entry and registers
nothing else; `autostart.Apply`; `autostart.Backend` is faked in the tests): the tray asks once
after the first successful connection (`tray/autostart.go`, a zenity question with the same
words; closing it counts as Not Now; with no dialog program nothing is recorded and it asks at the
next start), and not in a viewer tray next to the service (`OfferAutostart`); the menu's
"Start at login" checkbox and `otc-sync autostart on|off|status` record an answer too. Unlike the
Mac, an entry an older otc-sync made unasked is kept until the user answers (there is no store
rule on Windows/Linux, and silently dropping it would stop the sync after the next reboot for
someone relying on it); "Not Now" removes it. The systemd user service (`otc-sync service
install`) is its own explicit choice and isn't affected. Never run the Debug Mac app to test this:
it shares the bundle id, settings and login item with the owner's installed app.
**Adding a folder (two steps) and a folder's requests (issues #192 and #132).** Both apps add a
folder in two steps: first the kind (backup, two-way from this computer, two-way from the device,
each with its (i)), then that kind's options, and only then the folder picker. A backup offers
"Keep out of Images" plus a fixed, already-on "Upload only" line (a backup always is,
`markUploadOnly`); a two-way folder from this computer offers "Upload only" and "Keep out of
Images"; a folder from the device offers nothing (it keeps what is set for it there) and goes
straight to the device's folders. The Mac's `AddFolderChooser` is one inline view in the popover
(never alerts, which close it) with Back and Cancel and a "Choose Folder…" button that opens the
`NSOpenPanel`; options start unticked for each kind, and one the device can't do is disabled with
its "needs an update" caption. The tray has one "Add Folder…" item (`tray/addfolder.go`): a zenity
list of the kinds, then the options - one checklist on Linux (zenity `--checklist`, with Back),
one question per option on Windows, whose stock list dialog has no checkboxes (only Ctrl+click
multi-select): Yes / No / Cancel (MB_YESNOCANCEL; No is zenity's extra button, so Cancel, Esc and
closing the dialog stop the whole flow instead of meaning No; there is no Back) - skipped when the
kind has none or the device can't do them, then the folder chooser. "What Do These Do?" explains the kinds and the options. The CLI takes
`--keep-out-of-images` on `backup` and `add`, `--upload-only` on `add` (`backup --upload-only`
just says a backup always is); `add-remote` refuses both with an error naming where to change
them. The words are the same in both apps ("this Mac" / "this computer"): `UploadOnlyText` /
`engine.UploadOnly*`, `OutOfImagesText` / `engine.OutOfImages*`, the kinds' titles and (i)s.
Upload only's (i) says what it means for a two-way folder in one sentence: "Files you delete on
this computer stay on the device and aren't downloaded again; files added or changed on the
device still come down.", then that turning it off later brings back the files deleted here
while it was on. Each option only records a request with the folder (`config.Folder` /
`RemoteFolder` `.OutOfImages` and `.UploadOnly`, `StoredFolder.outOfImages`,
`StoredRemoteFolder.outOfImages` / `.uploadOnly`; nil = nothing to send, absent from older data;
a request an older version stored for a folder from the device is still sent once). Both kinds
share one machinery - otc-sync's `engine/requests.go` (`requestKind`, `requestState`,
`applyRequest`, `config.Request`/`SetRequest`/`ClearRequest`), the Mac's `RequestKind` (key paths
to each kind's state) and `sendRequest`. A request is sent for the folder's device path - at the
start of its passes (upload only first, before the listing; never holding the pass up), at
connect, and at once when config.json brings one or the row makes one - until acknowledged, then
cleared compare-and-clear (otc-sync in config.json first, retried like its reads, then in memory)
and never sent again: unlike `markUploadOnly`, re-sending at every start would undo a change made
since from the web or a phone. If config.json can't be written, the engine keeps the value in
memory marked answered (`requestState.acked`): no pass or reload sends it, the clear is retried at
the next pass and config.json reload, and only a different value (or a later request once the
clear is written) goes. One request per folder and kind at a time; a pass waits for one under way,
and a change made while a request is under way goes right after it, even when that request failed
(otc-sync releases the folder in the same lock hold as its last look at the request).
`unknown_payload` keeps it pending and marks the device unable for that kind until the next
connect (the option is disabled with "Your device needs an update to …", the tray doesn't offer
it, and a pending folder says so). The kind's by-parent code (`out_of_images_by_parent`,
`locked_by_parent` - only lifting upload only can get it, which no app asks) drops it and shows
the device's message on the folder; any other failure leaves it for the next pass. A pending
upload-only request shows as a lock on the Mac's row (`UploadOnlyBadge`, tooltip "Making it upload
only…", orange for a device that can't or a refusal) and " · Making it upload only…" in the tray's
folder title (the refusal on a line of its submenu); nothing once acknowledged. state.json has
`FolderStatus.UploadOnly` (`engine.UploadOnlyState`: making, lifting, unsupported) and
`UploadOnlyNote`, `State.UploadOnlyUnsupported`; `otc-sync folders` prints it.
**Keep out of Images (issue #192)** in particular: the add flow's caption says that tags and faces
already found are deleted (`engine.OutOfImagesAddCaption` / `OutOfImagesText.addCaption`): the
folder may already be on the device. Later, per folder - every folder, from the device too: the
eye button on the Mac's row (an inline confirmation, not an alert; the row has no text line about
Images - the owner found it crowded - so the eye's tooltip carries the state, a request on its way
and the device's refusal), the "Kept Out of Images" checkbox in the tray's folder submenu, and
`otc-sync images <id|path> keep-out|show`. `unknown_payload` is a device before release 108.
`out_of_images_by_parent` shows the device's message while a folder above still keeps it out (the
next list that says otherwise drops the message). `otc-sync images <id> keep-out` on a folder kept
out by a folder above says it already is (exit 0); `show` there is refused. What the device keeps
out comes from `ListOutOfImages` at connect, after each ack and with the one-minute poll,
forgotten when the device or password changes; a folder's state (`SyncPaths.outOfImagesState` /
`engine.OutOfImagesState`: shown, kept out, kept out by a folder above - the control disabled -,
keeping/showing while pending, unsupported, unknown = nothing shown) compares paths byte for byte.
`engine.migrateFolders` carries both requests. otc-sync's state.json has it per folder
(`FolderStatus.OutOfImages`, `OutOfImagesBy`, `OutOfImagesNote`) and `otc-sync folders` prints it.
**Upload only in a two-way folder (issue #132).** The device refuses a delete under an
upload-only folder (`upload_only`). Until 2026-10 the clients logged it and dropped the path from
the sync record, so the next pass took the file for new on the device and downloaded it back. Now
the refusal is recorded: the record entry becomes `kept-on-device:<device hash>`
(`keptOnDevice` / `SyncPaths.keptOnDevice`), which stands for two baselines -
absent here, that hash on the device (`recordBaselines`). While neither side changes the file
nothing happens (not downloaded again, the delete not sent again, restarts included); a new
version on the device comes down; a file put back here goes up (while the folder is upload only
the device keeps the old one as a version); both at once are a conflict like any other (a copy is
kept); gone from the device too, the entry goes. Upload only lifted later (from the web or a
phone): the listing's per-file `upload_only` (File field 9) is false for a kept entry, which then
has no baseline at all (`baselinesFor` / `SyncPaths.baselines`), so the file is downloaded back
here and the record is a plain entry again - the old delete is never sent, so nothing the lock
protected is deleted (owner's decision; the add flow's (i) says files deleted while it was on come
back once it is turned off). The delete is still sent once per file (the device decides, not the
listing's flag). While a folder's own Upload only request is pending and not acknowledged (a
failure, or a device that answers `unknown_payload`), its passes plan no device deletes at all
(`holdDeletes`, from `uploadOnlyAwaited` / `uploadOnlyPending[id] == true`): a file deleted here
keeps its baseline, nothing is downloaded back, and the delete goes once the flag is set - where
the device refuses it and it becomes kept-on-device. otc-sync logs the held count when it changes
(`noteHeldDeletes`); the Mac counts it in each pass's summary line. The mass-deletion guard is
unchanged (kept entries never become local deletes). An older client reading such a record sees a
hash that matches neither side and downloads the file, as before.

**Setting up a new device from a computer (issue #184).** Both desktop apps have "Set Up a New
Device…". They download `off-the-cloud-rpi-lite-arm64.img.xz` from the `image` release and trust it
only through the release-key signature over its SHA-256 (`.sha256.sig`, signed by `make
image-publish` via `scripts/sign-file.sh`). otc-sync (`internal/flasher`, tray `setup.go`, CLI
`otc-sync flash [disk]`) writes the card itself. It lists removable disks only:
- on Linux, sysfs `removable`, USB or `mmcblk`, minus any disk holding `/`, `/boot`, swap and so
  on, followed through device-mapper slaves;
- on Windows, `Get-Disk` with bus type USB, SD or MMC, not boot or system.

Every USB disk is listed, so a disk that holds a folder otc-sync syncs is refused
(`flasher.HoldsFolder`: on Linux what is mounted from it, through device-mapper; on Windows its
partitions' access paths; `desk.folders.setup_disk_has_folder`), one over 128 GB (`LargeDiskSize`)
is asked about a second time (`desk.folders.setup_large_disk`; `otc-sync flash` wants its size
typed), and on Linux the writer unmounts only what the desktop mounted for a card (`/media/…`,
`/run/media/…`): a disk mounted anywhere else is refused (`desk.folders.flash_card_mounted`).

The tray's list of cards shows Linux's full labels; Windows' list is 241 px wide and cuts what
doesn't fit, so there a row is the size, the disk's number and the name, at most 34 characters
(`windowsCardRow`; the device dialog's prompt there is the short `desk.main.settings_hint_windows`).
It needs 8 GB or more. Writing runs elevated as the hidden `otc-sync flash-device` (pkexec, or UAC
through `ShellExecuteExW runas`), which reports progress as JSON lines in a status file the
wizard polls. The elevated process re-checks that the disk is removable, fetches the signed hash
itself and hashes the image before and while writing. It prepares the card: Linux unmounts it
and opens it `O_EXCL`; Windows runs diskpart `clean` and opens the drive `NO_BUFFERING` with
page-aligned buffers. It blanks the first MiB, streams the xz (ulikunitz/xz), writes the first
chunk last so nothing mounts a half-written card, then syncs. On Linux it drops the cache
(`BLKFLSBUF`) and reads everything back against the decompressed hash before ejecting
(udisksctl power-off). The download is deleted after a successful write.

The Mac app (`SetupWizardView.swift`) is sandboxed and can't write raw disks. It downloads into
`~/Downloads` (`ENABLE_FILE_ACCESS_DOWNLOADS_FOLDER`) with the same checks, opens Raspberry Pi
Imager with the image (bundle `com.raspberrypi.rpi-imager`; it also selects the file in Finder for
"Use custom"), deletes the image after "I've Written the Card", and can continue over Bluetooth:
`BluetoothSetupView.swift` is the iOS file with an AppKit view layer, and finishing there points
the Mac's sync at the new device. otc-sync continues on the phone or through the hotspot
(`http://10.42.0.1/`), the parity exception the owner chose. Tested by flashing a loop device in
the Lima VM (`OTC_FLASH_TEST_DISK`, honoured only for root) and a virtual USB disk in the Windows
VM (`run.sh -drive file=card.raw,if=none,id=card,format=raw -device
usb-storage,drive=card,removable=on`). Its packages are one-to-one with the Swift files: `wsclient` =
WSClient.swift + PwCrypto.swift, `engine` = SyncModel.swift (upload folders with a watcher and a
10-minute reconcile, two-way remote folders with the three-way merge and a 1-minute poll,
hash-first uploads, RAID polling), `engine/chunked.go` = ChunkedUpload.swift (the chunked upload
and its resume, issue #205), `engine/watcher.go` = FolderWatcher.swift (fsnotify, one watch
per directory, added as directories appear; 4 KiB buffers on Windows, an overflow checks the
whole folder, as FSEvents' dropped-events flags do a backup's on the Mac,
`FolderWatcher.eventsLost`; a watch limit reached leaves the rest watched), `tray` = PopoverView.swift (fyne.io/systray menu,
the OS's own dialogs through ncruces/zenity - no GUI toolkit, no CGO), `config` = SettingsStore
+ the bookmarks (config.json, keyring or a 0600 `secret` file, state.json), `autostart` = the
registration half of LoginItem.swift (XDG autostart file / HKCU Run key, only with consent - see
"Start at login" above), `service` (systemd user unit + linger). One process runs the
engine (a flock in the config dir); a tray started next to the service is a viewer, and every
edit goes through config.json, which the engine watches - so the CLI, the tray and the service
never disagree. The engine's process writes state.json at every change and every 10 s
(`stateHeartbeat`): a viewer and `otc-sync status` take one older than 30 s for a sync that isn't
running, and a service with nothing to say (waiting on a wrong password, at rest) read as one. Every edit takes `config.lock` (`config.Edit`; flock, LockFileEx on Windows) and is
written through a temp file of its own, fsynced and renamed (`WriteFileAtomic`, also state.json,
records, caches); the file replaced is kept as `config.json.bak`, which `Load` reads when
config.json isn't valid. One that can't be read starts the tray with "The settings can't be read"
and "Reset Settings…" (`config.Reset` renames it aside and forgets the password, the old
device's). The tray and the service log to `<config
dir>/otc-sync.log` (5 MiB, one `.1` kept; the tray's "Show Log" - the Mac logs to the unified
log instead, `log show --predicate 'subsystem == "cloud.off-the.OffTheCloud"'`); on Windows the CLI subcommands
attach to the terminal's console (`attachConsole`). A keyring still locked at boot shows "Waiting
for the password" and is asked again every 30 s; `otc-sync service install [--password-file]`
offers to keep the password in the 0600 `secret` file instead (`Config.PasswordInFile`). Remote paths are `/linux/<host>/…` and `/windows/<host>/C/…`, like `/mac/<host>`.
Any behaviour change in the macOS app must be mirrored here (and vice versa), the same rule as
iOS/Android. There are three kinds of folder, each explained in the app (the Mac's
`AddFolderChooser` with an (i) per kind and option; the tray's "What Do These Do?"):
**backup** (one way and upload only, `TrackedFolder` / `config.Folder{OneWay: true}`, `otc-sync
backup`: new and changed files go up, nothing is ever deleted on the device, and nothing on the
device ever changes the folder. Its device folder is made upload only at every start,
`markUploadOnly`, so the device keeps older versions and refuses deletes), and two
**two-way** kinds - from the computer or from the device - which only differ in their first pass
(what is only on one side is copied to the other; with no sync record yet nothing is ever
deleted). Both directions have a mass-deletion guard (more than 20 files and a quarter of the
folder in one pass): a two-way folder restores what the device lost instead of deleting it
locally, and holds the device deletes of files gone from here (they stay there with their
baselines, `desk.main.deletes_held`, until they are back here or deleted there too; content here
under another name - a move - doesn't count; `holdMassDeviceDeletes` / `SyncPaths.heldDeviceDeletes`); a backup keeps on the device what vanished locally.
**"Delete Them on the Device Too…"** (owner-approved, 2026-11-04; `engine/deleteheld.go`,
`SyncPaths`' "Delete Them on the Device Too…" section): the way out of that hold from the computer.
While the engine holds a folder's deletes it publishes how many and the set's key
(`config.FolderStatus.DeletesHeld`/`DeletesHeldKey`, from `noteMassHeld`; the Mac's `@Published
heldOnDevice`), and the folder offers the action: the Mac a link-style button under its row
(`DeleteHeldButton`, then the inline `DeleteHeldConfirmation`, never an alert; it closes if the held
set changes while it shows), the tray an item after Open in its submenu (`tray/deleteheld.go`: a
zenity question with a warning icon, Cancel the default), and `otc-sync delete-held <id|path>
[--yes]` (asks for a typed yes; `otc-sync status`/`folders` show "(N kept on the device - to delete
them there too: otc-sync delete-held <id>)"). Words: `desk.main.delete_held`, `_tip`, `_title` ("Delete
{files} files on the device too?"), `_message`, `_button`. A yes approves the set it asked about,
never whatever is gone by the next pass: the key is the SHA-256 hex of each held path and the
device's hash of it, one `path\0hash\n` line each, sorted by bytes (`heldDeletesKey` /
`SyncPaths.heldDeletesKey`, byte for byte the same - both tests hold one value), recorded in
otc-sync's config.json with the folder (`RemoteFolder.DeleteHeld`, `SetDeleteHeldRequest` /
`ClearDeleteHeldRequest` compare-and-clear, like the requests: the tray may be another process) and
in the Mac's memory (`deleteHeldApprovals`). A config.json that brings one runs the folder's pass at
once (`changedApprovalsLocked` -> `passSoon`), or once the pass under way is over (`passAgain`; the
Mac's `deleteHeldOnDevice` and `remotePassAgain`). At the guard the pass judges it
(`judgeDeleteApproval` / `SyncPaths.heldApproval`): the key of the set held now - those deletes go,
still subject to everything after the guard (an unconfirmed pin holds them like any delete and keeps
the approval for the pass that lets them go); another set, or nothing held - nothing is deleted on
the device and the hold stays, showing the new count to be asked again. Either way the approval is
cleared and logged once (`settleDeleteApproval`); one read back from a config.json whose clear
couldn't be written is never used twice (`approvalDone`). While an Upload only request is pending
no device delete is planned at all, so an approval then finds nothing held and is dropped.
What a two-way pass deletes here goes to the trash: the Mac's Trash (`trashItem`; on a volume that
has none, an SMB or NFS share, the same `.otc-trash/<day>/` folder as otc-sync's below, pruned the
same way: `SyncModel.trashHere`, `SyncPaths.folderTrash` / `pruneFolderTrashes`), and otc-sync's system trash
(`engine/trash*.go`: the freedesktop.org trash on Linux - the home trash, or the disk's own
`.Trash/<uid>` or `.Trash-<uid>`, each file with its `.trashinfo`; what otc-sync put there is
emptied after 30 days, as a service with no desktop has nobody to do it - only its own entries,
listed in `<config dir>/trashed.jsonl` and still as it left them (`pruneOwnTrash`) -, the Recycle
Bin on Windows, fixed disks only and only for a file the bin keeps - the shell deletes for good,
under the silent delete's FOF_NOCONFIRMATION, a file larger than the bin and every file where it is
off, so the volume's `BitBucket` settings (NukeOnDelete, MaxCapacity; else 5% of the volume) and
the NoRecycleFiles policy are read first (`recycleBinTakes`); else a `.otc-trash/<day>/` folder in
the synced folder - at the top of a disk mounted in it for a file on that disk, which a rename
can't leave -, never synced, each emptied of days older than 30 after a pass,
`pruneFolderTrashes`); a file that can't be moved stays, and is tried again. Upload-only folders from before backups were a choice were migrated to two-way once
(`SyncModel.migrateLocalFolders`, gated by `sync.folders.migratedToTwoWay`; `engine.migrateFolders`
skips `OneWay`), and the sync record (relative path -> hash after the last pass) is saved per folder
(`synced/<id>.json`), so a delete made while the app was closed still propagates. Issue #134: both send a file's own creation and modification times with
`UploadFile`/`LinkFile` (`created`, `modified`; the device keeps them as the row's dates instead
of the upload time) and set them back on a downloaded file (`SyncModel.download` /
`engine.download` via `times_*.go` - creation time only where the platform can set one:
macOS and Windows; Linux has no birth time to read or set, so there `created` is the mtime).
The two-way conflict rule compares local mtime with the device's `modified`, which is why a
downloaded file must carry the device's time and not "now". Both keep their local hash cache on
disk too (`Application Support/OffTheCloud/hashes/<folder id>.json` on macOS, `<config
dir>/hashes/<folder id>.json` for otc-sync; path -> size, mtime, hash), so a relaunch doesn't
re-read every file - the reference two-way folder is 66 GB - just to confirm nothing changed;
the checking pass shows "Checking i/N · name" while it runs (#138). Next to the storage health both show how full the device is and its load: on the Mac "Storage healthy · 15% used" (`StorageStatusView`: one text that wraps after the dot rather than cut, for a longer health or language) with storage, CPU and memory in a pop-up on hover (`DeviceLoadView`); in otc-sync a text bar in the storage line (`storageTitle`) whose submenu, opened on hover, holds the same three, also its tooltip (`config.State.StorageUsed`/`CPUPercent`/`MemUsed`, from the same status poll). Storage comes first, used of total ("39.6 of 474 GB", `storageUseTitle`), and only once the device reports a size; on the Mac its bar turns orange past 75% and red past 90%, like the line (`fullnessColor`). Storage and memory share one number format (`sizeText`/`usedOfTotal` in both apps): one decimal under 100 GB, whole GB from 100 GB, TB with one decimal from 1000 GB ("1.2 of 3.6 TB"; "39.6 GB of 3.6 TB" when the units differ). Conflicts in a two-way folder (both sides changed a file since the last sync) still go to the newer
modification, but the losing version is kept next to it as `name (conflict from <computer>
<yyyy-mm-dd hh.mm>).ext` (this computer's version) or `name (conflict <date>).ext` (the device's),
and then syncs like any new file. The steps are ordered so a failure loses nothing: rename, then
download (otc-sync `actDownloadKeepLocal`); or download the other version to the copy, then
upload (`actUploadKeepRemote`). On the Mac these are `.downloadKeepLocal` / `.uploadKeepRemote`
and `SyncModel.conflictURL`. Before this, the older edit was silently lost. In a two-way pass an entry the device lists without a hash (content lost, #141) is uploaded from the local copy and never downloaded - taken for a device-side change, it failed on every pass and flooded Alerts. A folder at rest that starts a pass keeps showing it is synced for 1.5 s (`setState` in both, `quietPassDelay`), so a pass that sends one changed file doesn't flash "99%". The status reports storage for the storage path's disk (the OS disk when there is none) in units of 1.024 MB. Test on Linux with the Lima VM `otc` (`limactl shell otc`, `limactl copy
dist/otc-sync-linux-arm64 otc:/tmp/`); the tray needs a real desktop - the Lima VM is headless, so the
Linux tray is tested on a real machine. Windows is tested in the QEMU VM under `~/VMs/win11`
(`./run.sh` starts it: Windows 11 ARM64, user `otc`, SSH on `127.0.0.1:2222` with the Mac's key,
VNC on `127.0.0.1:5905`; `./qmp.py screenshot|type|combo|click` drives the screen through QMP,
`scp -P 2222` copies builds in; the tray app has to be launched on the desktop, e.g. through
`qmp.py combo meta_l+r` + `qmp.py type`, since an SSH session has no desktop). It was installed
unattended from Microsoft's Enterprise Evaluation ISO (`unattend/autounattend.xml`), whose
licence has expired - Windows shows a watermark and may shut the VM down periodically; if that
gets in the way, rebuild it from a retail ISO (CrystalFetch) with the same answer file.

**Sync invariants** (otc-sync `engine`, the Mac's `SyncModel` + `SyncPaths.swift`). A two-way
device entry is used only under the folder's prefix and through `safeRelative` (no empty, `.` or
`..` component, no NUL; the Mac checks by UTF-8 bytes, since a combining mark after `/` hides it from
a Character split). What is never synced, either way, is one list (`engine/skip.go` /
`SyncPaths.isExcludedFromSync`): hidden paths (a `.` component; what Finder hides on the Mac and
the Hidden and System attributes on Windows are not synced either, as skipped - below), `.otc-part` files, and Office's and Explorer's scratch
files (`~$*`, desktop.ini, Thumbs.db, ehthumbs.db, ehthumbs_vista.db, any case). Paths are keyed
in NFC (`pathKey` / `SyncPaths.key`, records too): what the device has is addressed by its own
bytes, a new file goes up in NFC, so an NFD path the Mac sent before is never renamed or deleted
for it, and of two device entries differing only in normalization the NFC one is synced, the
other left alone. Two-way actions run local deletes first and re-check the local file's size and
mtime just before acting (`stillAsScanned` / `localMatches`), and the device's file right before
an upload replaces it (before LinkFile and FinishUpload) or a delete removes it, by listing that
one path (`deviceStillAsListed`): changed since the listing, it is left for the next pass, which
keeps both versions. A sync record (`synced/<id>.json`: `{"v": 2, "device": {"domain", "pin"},
"root", "mounts", "maybe_mounts", "files"}`) names the device it was made with - its home-network certificate pin
as this sign-in confirmed it (otc-sync's `wsclient.SessionPin`, the Mac's `WSClient.sessionPin`: the
home route's pinned handshake, or the device's own GetLocalEndpoint answer, asked again at most once
a minute by `ConfirmPin` / `confirmPin`; never the pin stored from an earlier sign-in), else the
address (`recordDevice`); one of another
device counts as none (that pass deletes nothing), an older bare map as the configured device's.
A record that names its device by a pin, while none is confirmed and the address is the same, is
uncertain (`recordForDeviceLocked` / `SyncPaths.recordFor`): the device, or one set up again since under its name. The
pass holds what it can't take back - deletes either way, downloads over a file here - with their
baselines, says so (`desk.main.device_unconfirmed`), and the record keeps its pin. The
mass-deletion guard doesn't count deletes whose content the device lists at a new path (a move
there). A folder whose own folder is missing waits ("Folder not found - reconnect the drive"),
untouched on both sides, and resumes once it is back (the Mac resolves its bookmark again and
follows a move; one that didn't resolve at launch stays listed); it is never removed. So does a
folder that is there but isn't the synced one (otc-sync `engine/rootid.go`; the Mac's
`SyncPaths.judgeRoot`, `SyncModel.identifyFolder`): each two-way folder
holds a hidden `.otc-sync-<folder id>` whose token the record keeps (`root`); without it (a record
from before, the file deleted) the folder must still hold at least half of the files the record
lists, unchanged - then the file is written again - or it waits the same way ("Not the folder that
was synced - reconnect its drive", `desk.main.folder_replaced`): the empty mount point of a disk or
share that isn't mounted, another disk at the same drive letter or volume name, a folder made
afresh, the empty Documents macOS leaves when iCloud's Desktop & Documents is turned off. The check
needs nothing from the device and comes before the listing; a folder that waits sends nothing, and
its watcher stays stopped. The marker is deleted with the folder from the app (off the engine's
lock / the main actor). On the Mac, a folder whose marker isn't the record's follows its bookmark
when that finds the folder holding it (renamed away, a fresh one made at its path:
`followMarkedBookmark`), and a folder picked again where it waits (`relink`: missing or not the
synced one) keeps its record only when it passes the check - else its record goes, with the mounts
it held (`SyncPaths.localView` without it), and that pass, a first pass, deletes nothing (`adoptOnce`). A disk or share mounted inside a two-way folder or a
backup is synced with it: the scan goes into it as into any folder (`filepath.WalkDir`; the Mac's
`FileManager` enumerator lists a mount point as an empty folder, so `SyncPaths.scanFolder` walks
each one it finds on its own). A folder
inside it that is another file system (st_dev; the record's `mounts`) and is a plain folder again
holds what is under it like an unreadable one (`mounts.go`, `SyncPaths.goneMounts`). A record from before `mounts` knows of
none, so at its first pass a folder the record lists files under, with none found under it now, a
plain folder not changed since the record was written - a mount point nothing touched while its
disk was away; one emptied by hand since is newer - is held the same way (`maybeMounts` /
`SyncPaths.maybeMounts`, the record's `maybe_mounts`), as is, on Linux only, an empty mount point
that `/etc/fstab` or a systemd mount unit declares there (`declared.go`), until files are there
again or the record lists none under it. The folder's identity still counts their files. What the scan skips but is there - a
symbolic link, to a file or a folder, anything that is neither a regular file nor a folder, on
Windows the Hidden or System attribute, on the Mac what Finder hides (UF_HIDDEN) - is never taken
for deleted (`folderScan.skipped` / `SyncPaths.FolderScan.skipped`; the Mac's scan no longer uses
the enumerator's `.skipsHiddenFiles`, which dropped those without a word),
`desk.main.files_not_synced_here`), and a device delete checks, right before it goes, that nothing
is here under that very name (`presentAsNamed` / `SyncPaths.presentAsNamed`: each component as its folder lists it, in NFC - on a
disk that ignores case the old name finds the new one after a rename made here only in case, which
goes up as an upload and a delete; what it can't tell - a folder on the path that can't be searched
or listed, a failing volume: any error but nothing there - holds the delete, counted as failed). A download is refused before it starts when the disk can't take it (its free
space less 256 MB, FAT's 4 GiB; `roomFor` / `SyncPaths.roomProblem`; the Mac takes the most free
space any of the system's measures gives, what macOS frees for an important file included,
`SyncPaths.volumeLimits`); a transfer that failed for a reason of its own waits as a busy file does
(`fail.go`; the Mac's `failWait`, `noteFailed`, logged with `syncLog.error`, not `print`), and the
pass ends saying so (`desk.main.no_space`, `desk.main.too_large_for_disk`,
`desk.main.files_failed`; which note wins: `SyncPaths.passNote`); the copy of the device's version in a
conflict is made once, reused while its upload fails (`conflictCopyOf`), and not at all for a busy
file. On a disk that ignores case, a folder renamed only in case on the device is renamed so here
before the pass plans (`casefix.go` / `SyncPaths.matchFolderCase`); one renamed so here - the record has its files under the
device's spelling only - is left as it is, and goes up as any rename. On Windows, device names Windows can't hold
(`\ < > : " | ? *`, a last dot or space, device names - with an extension only where that Windows
says so, through `filepath.IsLocal`: Windows 11 holds `con.txt`) are left out either way: no
action for their paths here either, a file here at one kept with its record (`names.go`,
`excludedHere`, `desk.main.names_not_allowed`; the Mac leaves a device entry it refuses as unsafe
alone here the same way). Tests: engine `rootid_test.go`, `skipped_test.go`, `trash_test.go`,
`fail_test.go`, `casefix_test.go`, `names_test.go`; the Mac's `SyncPathsTests` and `WSClientPinTests`; the engine's tests run with HOME and the XDG
folders in a scratch folder (`TestMain`), the config and command line's against an in-memory
keyring. Downloads go to a temp name of their own (`name.<random>.otc-part`),
synced before the rename. Two folders' passes run at a time (`passSlot`; the loops start at once),
the others say "Waiting for <folder>". A file that changes while it is sent is a busy file, left
for 2 minutes, doubling up to an hour. Uploads resume (issue #205; `engine/chunked.go` /
`ChunkedUpload.swift`): every BeginUpload carries the content's `sha256` (the hash it was hashed
with; with path and size the device's resume key) and the upload goes on from
`UploadStarted.received`, the file read from that offset on, never the part the device holds
(nothing is hashed again for it). A connection that drops once the device has begun the upload
(`wsclient.Dropped` / `WSClient.isDropped`: no connection, a write that failed, the socket
replaced - not a request's timeout, nor the Mac closing it itself) waits, 2 minutes at most
(`resumeWait`), for the client's next sign-in (`SignIns` / `signInCount` moved on, the same device
still set, the engine not stopped), then sends the same BeginUpload again; after 3 drops in a row
that left the device holding no more of it (`uploadResume` / `UploadResume`, `maxStalledResumes` /
`maxStalled`) it gives up, so a big file on a link that keeps dropping finishes as long as each
connection gets some of it through. A file whose size or mtime changed meanwhile is a busy file -
its new hash is another key and starts fresh -, a drop during FinishUpload isn't resumed (the next
pass's HasFile or BeginUpload finds out), a device before release 166 answers received 0 (the file
starts again), and FinishUpload's hash check is unchanged. Tests: engine `resume_test.go` (the
fake device keeps uploads by key and drops connections), wsclient `resume_test.go`,
`ChunkedUploadTests` (below). The hash cache also checks the change time (not on
Windows, which has none without a handle per file) and is saved every 5 minutes during a pass and
at Stop / quit. Both WSClients ping every 20 s and drop a connection silent for 45 s (a send under
way has its own deadline); small requests wait 2 minutes, listings and transfers 10; otc-sync
takes a clock jump between its 5 s network checks for a wake. The device has no empty folders, so the folders a pass's local deletions emptied
are removed after it (`removeEmptiedDirs` / `SyncPaths.removeEmptiedFolders`): up to, never including,
the synced folder, only while they hold nothing but a `.DS_Store`, with rmdir semantics. An unreadable directory is never a deleted one: paths under it keep their baseline
and get no action (both report "N file(s) could not be read", `desk.main.files_unreadable`). Backups run one pass per
folder, watched changes one upload at a time per folder (`drainChanges`), and a pass stops when its
folder is removed or the device changes. A backup's `SetUploadOnly` is re-sent until acknowledged,
and that is forgotten only when the device or password changes. A two-way record entry
`kept-on-device:<hash>` (a delete the device refused) is never a plain hash: read it only through
`baselinesFor` / `SyncPaths.baselines` (which also drop it once the folder is no longer upload
only). A two-way folder whose Upload only request isn't acknowledged sends no device deletes.
**Compress (computers keep their originals, owner 2026-11-08):** a two-way device entry listed with
`original_removed` whose `original_hash` is the local hash, or the record's plain entry, is the same
file: nothing is downloaded or sent, and the record keeps `compressed:<hash here>:<hash on device>`
(read only through `recordBaselines`/`baselinesFor` / `SyncPaths.baselines`, written back by
`recordEntry`). A new version here goes up and replaces the copy; a later change of the copy (an
edit) comes down. A copy the device saved under another name (x.png -> x.jpg, "x (2).jpg", a HEIC ->
x.JPG) is paired with the original here (`renamedOriginals`, same folder, `stemOf`/`originalStems`
as the device's `splitExt`/`renamedNames`) and planned as one file at the name here; changed here,
the copy is deleted first and the new version goes up under the name here; the copy edited, deleted
or renamed on the device breaks the pair (it comes down, the original goes to the trash). An upload
whose content is the original of a listed compressed copy links the copy instead (`linkCopy`: the
device would answer it with the copy and store nothing). Two-way passes run device deletes last.
Backups count a file as backed up when the device's file at its path, or under the name an edit or
compress gave it ("(n)" included), was made from it. Folder identity and both mass-deletion guards
treat a compressed copy as its original. Tests: engine `compress_test.go`, the Mac's
`SyncPathsTests` (same case names). A sync record counts as saved only after a
successful write. In both WSClients a superseded connection attempt's outcome is dropped, and a
rejected password is reported before the socket closes. `otc-sync flash-device` refuses a disk
whose `--size`/`--name` changed. The Mac's unit tests are hosted by the real app (`TEST_HOST`), so
`xcodebuild test` launches it with the saved settings and may reach the live device;
`SyncPathsTests` can run in a scratch SwiftPM package holding only `SyncPaths.swift` (module
`OffTheCloud`), `LoginItemTests` likewise with only `LoginItem.swift` (a fake login item and a
throwaway defaults suite), `FolderWatcherTests` with only `FolderWatcher.swift`, and `ChunkedUploadTests` (and `WSClientPinTests`, `WSClientStorageTests`)
with `WSClient.swift`, `ChunkedUpload.swift`,
`LocalRoute.swift`, `PwCrypto.swift`, `messages.pb.swift`, `i18n/*.swift` and an in-memory
`Keychain` stand-in (SwiftProtobuf from the Xcode build's checkout, Swift 5 mode with the target's
approachable-concurrency features) against a loopback stand-in device. `SyncPathsTests` work in
the temporary folder: they make links, a FIFO, folders hidden with `chflags`, one without
permissions, rename a folder in case where the disk ignores case, and attach two 4 MB disk images
inside it with `hdiutil` (detached at the end; that test is skipped inside the app's sandbox).

### Native apps (`app/ios`, `app/macos`, `app/android`)

Swift/Xcode projects (`OffTheCloud.xcodeproj` in each) that consume the same generated Swift protobuf
messages (`make pb` copies `messages.pb.swift` into both) to talk to a device or the bridge over the
same `/ws` protocol.

`app/android` (issue #88) is the Android app: Kotlin + Jetpack Compose, a 1:1 port of the iOS app
kept in its own directory so the two are worked on independently - port screen by screen, keeping
the iOS file/type names (SwiftUI view ↔ Composable, ObservableObject ↔ ViewModel, Keychain ↔
EncryptedSharedPreferences, PhotoKit ↔ MediaStore, BGTaskScheduler ↔ WorkManager, APNs ↔ FCM).
Android's background sync (`sync/SyncScheduler.kt`) has more than iOS can: besides the 15-minute
periodic run, a MediaStore content trigger (images and videos, 5 s update delay, 1 min max) fires
`MediaWorker`, which arms the next trigger (APPEND_OR_REPLACE behind itself, never REPLACE: that
cancels the running worker) and queues a one-time `SoonWorker` sync (network required; it retries
while another sync runs in the process). Armed by `scheduleNext()` on every backgrounding,
re-armed by WorkManager after a reboot, cancelled at Log Out. A big run with the app on screen
goes on as `LongSyncWorker`, a foreground job WorkManager doesn't stop (issue #205, below).
`make pb` writes its protobuf bindings (`--java_out=lite` + `--kotlin_out=lite`, package
`cloud.offthe.otc.proto`, one file per type) to `app/android/app/src/main/proto-gen/`, which is
gitignored like `proto/generated`. The root `.gitignore` ignores the device binary as `/otc` - anchored, because the
unanchored `otc` it used to be also swallowed the `cloud/offthe/otc/` package directory and the
first Android commit went out with no Kotlin at all; check `git ls-files app/android | grep .kt`
after adding sources. Because those generators emit one file per type, no two proto
type names may differ only by case (the message `FriendshipStatusReply` was renamed for exactly
that: it collided with the enum `FriendShipStatus` on macOS's case-insensitive filesystem). The
protobuf runtime version in `app/build.gradle.kts` must match the installed `protoc` (4.<protoc
minor>, e.g. protoc 36.2 ↔ `protobuf-kotlin-lite:4.36.2`). Build with `cd app/android && ./gradlew
assembleDebug` (the wrapper pins Gradle 9.1; the Homebrew `gradle` is too new for AGP 8.13 and is
only used to generate the wrapper). Toolchain on the Mac: `openjdk@21`, `android-commandlinetools`
+ `sdkmanager` packages (platform 36, build-tools 36, emulator, `system-images;android-36;
google_apis;arm64-v8a`), Android Studio; `JAVA_HOME`/`ANDROID_HOME` are set in `~/.zprofile` and
`app/android/local.properties` points at the SDK. An emulator named `otc_pixel` (Pixel 8, API 36)
exists: `emulator -avd otc_pixel`, then `adb install -r app/build/outputs/apk/debug/app-debug.apk`
(adb on macOS tends to flip to "offline" right after a large install - retry in a loop rather than
assuming the install failed). For a real phone over USB, the SDK's adb 37.0.1 crashes on macOS 27
(`libunwind: stepWithCompactEncoding` in `usb_osx.cpp`) the moment a phone is plugged in; the
35.0.2 platform-tools from Google's archive (`platform-tools_r35.0.2-darwin.zip`) work - keep them
outside the SDK dir so `sdkmanager` doesn't overwrite them, and kill the 37 server first. The test
phone is a Motorola moto g06 (Android 15, serial ZY32MBZVJ4). Every iOS screen has an Android counterpart under
`app/src/main/java/cloud/offthe/otc/` (`ui/` mirrors the SwiftUI views, `net/` the connection,
`data/` the stores, `sync/` PhotoSync/SyncScheduler/AssetSyncCache); still missing versus iOS:
push notifications (FCM). Leaving a device from the "isn't available" card (its ✕) or "can't connect" card ("Set up or
connect a different device") wipes the phone's state the same way Log Out does. It keeps the
device's address and password as the *last device* (Keychain / EncryptedSharedPreferences
`last_endpoint`/`last_password`; `AppLogOut.run(keepDevice:)` / `logOut(keepDevice =)`), so
Onboarding fills them in: a device that is down is usually the one the phone comes back to.
Settings' Log Out forgets them too. iOS Keychain items are `AfterFirstUnlockThisDeviceOnly`:
items readable only while unlocked used to make a background launch on a locked phone (the photo
sync) read an empty address and show the connection screen. Older items are rewritten once
(`keychainAfterFirstUnlock`), and the device id is never replaced because it couldn't be read.
Release builds (issue #129, `private/stores/play-store.md`): R8 minify and resource shrinking on,
signed with the upload key `~/.otc/otc-upload.jks` (password in the Keychain, service
`otc-android-upload`, passed as `OTC_UPLOAD_PASSWORD`; never in the repo), `versionCode` = the
commit count; `-Potc.signWithDebug` signs a release build with the debug key to try it over an
installed debug build. The launcher icon is adaptive (`mipmap-anydpi-v26`). One deliberate difference: the photo sync's cloud-id shortcut (release
7, `files.cloud_id`, `HasCloudIds`) is iOS-only - `PHCloudIdentifier` is the same for an asset on
every device on the owner's iCloud account, so a phone asks the device "which of these do you
already have?" and links the hash instead of downloading the asset from iCloud to hash it. Android
has no equivalent (MediaStore ids are one phone's row numbers, and the originals are local files
anyway), so it leaves `cloud_id` empty; the device attaches an id to every row of a hash whenever
any client names it (`HasFile.cloud_id`), so content uploaded from Android still gets one the
first time an iPhone sees the same asset. The EXIF panel's map is Places' (MapLibre on OpenFreeMap, `LocationMap` in
`PlacesMap.kt`, still, a dot at the point), and it
only ever has something to show because `PhotoSync.readData` asks the MediaStore for the
original bytes under `ACCESS_MEDIA_LOCATION` - since Android 10 a photo read through a plain
content URI comes back with its GPS tags blanked to 0/0, which the device used to report as
NaN. Two Compose pitfalls hit here: a `PlayerView` in a `LazyColumn` must be inflated from
`res/layout/player_texture.xml` (`surface_type="texture_view"` + `clipToBounds()`), or its
SurfaceView paints at a stale position over whatever scrolls above it, and a full-screen `Dialog`
with `decorFitsSystemWindows = false` needs `systemBarsPadding()` or its top buttons sit under
the status bar and never get the tap. A third: a `Dialog` shown outside MainView (from RootView, as
SyncChoiceView) must draw a `Surface`, not a `background()`, or its text takes the default black
content colour and vanishes in dark mode.

**What to sync, asked at every sign-in** (iOS `SyncChoice.swift` <-> Android `ui/SyncChoice.kt`):
Onboarding's Save & Continue and the Bluetooth setup's "Use this device" call `SyncChoice.ask()`
before saving the credentials, and a screen that can't be dismissed asks for all photos and videos
(= Sync All: watermark cleared) or only new ones (= Sync From Now), with Wi-Fi only, Include videos
and (iOS) Sync from iCloud, and a warning that a whole library takes long and uses data.
Until it is answered (`syncChoicePending`, UserDefaults / the `otc_sync` prefs) `PhotoSync.runForeground`
returns at once - background runs included. No run asks for access: Photos/media are asked from Start
Sync (iOS registers its PHPhotoLibrary observer only once allowed - registering asks), notifications
once after the first accepted sign-in and the tour, with what they bring (`NotificationsAsk`).
**Another address in Settings** (iOS `DeviceChange.swift` <-> Android `data/DeviceChange.kt`): Settings ›
Connection's Save, or the "can't connect" card's Save & Retry, with another address (not the same
one respelled, nor a first sign-in) may be another device or the same one reached another way. The
screens are made afresh at once (`addressChanged`: the session models Log Out resets, and RootView
makes MainView again, `SecretsStore.deviceSession`; the wallpaper's work under way stops writing,
`WallpaperModel.addressChanged`, as its next requests go to the new address), Settings' Save first
has the device left forget this phone's push token (`unregisterPushBeforeLeaving`, 3 s at most), and
until the new address's device says who it is (`DeviceChange.pending`: the last device's pin kept in
UserDefaults) no photo sync runs (`onePass` returns skipped) and the wallpaper isn't kept in step.
Its GetLocalEndpoint answer decides (`refreshLocalEndpoint`; asked again 5 times, 30 s apart, when
it says nothing): the same pin - nothing changes and the sync goes on; another pin,
`unknown_payload`/`local_unavailable`, or no pin known for the last one - another device: the run
stops, SyncChoice is asked as at a sign-in, and the wallpaper stops as at Log Out (`apply`). Until
then the last device's watermark carried over, so the new one never got the photos taken before it.
Leave the Bridge is the same device (`saveCredentials(sameDevice:)`): no check, and its pin moves to
the new address. Tests: `DeviceChangeTests.swift`, `WallpaperTests`'
`anotherAddressSavedStopsAKeepInStepUnderWay`. Android: the check's pin is kept in the `otc_sync`
prefs (`deviceChangePin`, hex; wiped at Log Out, not backed up); `SecretsStore.deviceSession` keys
MainView in RootView, whose view models live in a store of that session (`SessionStores`, an
Activity view model, so a recreation keeps them); the screens' singletons reset as at Log Out
(`DeviceChange.addressChanged`) and the feed and the Alerts poll start again; Settings' Save sends
`UnregisterFcmToken` over the socket in use first, then saves, in the connection's own scope
(`OTCConnection.saveAnotherAddress`, `SaveAnotherAddressTest`: in the screen's, leaving Settings
during those seconds dropped the save after the token had gone); `PhotoSync.onePass` is skipped while
`DeviceChange.pending`; the wallpaper (`Wallpaper.addressChanged`: its generation bumped and the
copies' fetch cancelled, nothing deleted) isn't fetched meanwhile and goes on rotating its copies,
then `sameDeviceAt` (the stored endpoint follows, a fetch is queued) or `deviceChanged()` (stopped);
Leave the Bridge and the same address respelled keep it (`sameDeviceAt`). Tests:
`DeviceChangeTest.kt`, `PinnedAddressTest.theSameDeviceAtAnotherAddressKeepsItsPin`.
**Folders and albums to back up** (Android `sync/PhotoFolders.kt` + `ui/settings/FoldersView.kt` <->
iOS `PhotoSources.swift` + `AlbumsView.swift`; owner's request 2026-10-24, after a user's folders seemed
missing): Settings > Sync Options > Folders (Android: MediaStore buckets by `BUCKET_ID`, with path and
volume) / Albums (iOS: the owner's albums plus Screenshots, Selfies and Screen Recordings), each with a
count and a switch. All on by default and stored as what to SKIP (`skippedFolders`), so a folder a phone
starts using later is backed up; nothing stored and no extra clause while none is off. Turning one back on
queues a one-off pass over it with no date limit that never moves the watermark; turning one off deletes
nothing, and Free Up Space leaves skipped folders/albums out. Limited photo access (Android 14+ selected
photos, iOS `.limited`) shows a warning with Select More Photos / Allow Access to All Photos; picks made
later and what was hidden before full access are backed up from the owner's chosen starting point. iOS
also reads the library's persistent change history (`PhotoHistory.swift`) so a photo added late with an
older creation date (AirDrop, Files) isn't behind the creationDate watermark; a missing token keeps the
old behaviour. Android's DATE_ADDED filter never had that gap. Include videos turned back on queues a
one-off pass over the videos since the owner's chosen starting point (iOS `PhotoSources.videos`,
Android `PhotoSync.VIDEOS` in the `otc_sync` one-off list; both `PhotoSync.videosTurnedOn`, from
the switch while signed in): the runs while it was off moved the watermark past the videos taken
then, which were never sent; none without a floor, as the catch-up. Tests:
`PhotoSourcesTests.theVideosPassFollowsTheFloor` <-> `PhotoSyncPlanTest.theVideosPassFollowsTheFloor`.
**Free up space** (iOS `FreeUpSpace.swift` <-> Android `sync/FreeUpSpace.kt`; owner's request
2026-10-21): off unless turned on (Settings > Free Up Space, and the same section on the sign-in
sheet), it deletes from the phone the photos and videos taken more than N days ago (1-365, 7 by
default) that the device holds an exact copy of. It runs at the end of a sync that ran
(`runForeground` -> `afterSync`, after the sync's own flag is down) while the app is in front, at
most once a day (`freeUpLastRun`; a run that found nothing or whose system question was answered
counts, one cut short doesn't; turning it on or changing the days makes it due). An asset goes by
the SHA-256 of what the sync sends, and only once the device confirms that hash on its disk right
then: `HasHashes` (140, answers `HashesFound` 70, 500 a request, up to 1000 looked at): a `files`
row with the hash and its blob at exactly the row's size sealed in the blob's own format
(`Format.EncryptedSize`, the format from its first 4 bytes: OTS1 and OTS2 headers differ) - a stat
and a 4-byte read, never a decrypt; older devices answer `unknown_payload` and the phones fall back to `HasFile` per hash. iOS
reads each original right before deleting it, only when it is on the phone (never from iCloud:
those stay), as a recorded hash may come from the device's cloud-id map rather than the bytes -
AssetSyncCache's hash only spares reading what the device doesn't hold - and never deletes edited
assets (the sync sends the original),
bursts, RAW+JPEG pairs (an `.alternatePhoto` or a second photo resource: the sync sends one of the two,
and deleting the asset deletes both), Live Photos unless `freeUpLivePhotos` (the sync sends only the
still), or videos while Include videos is off; `PHAssetChangeRequest.deleteAssets` asks every time and keeps them 30 days
in Recently Deleted (with iCloud Photos they leave iCloud too). Android re-reads a file whose
`GENERATION_MODIFIED` or `SIZE` changed since it was hashed (its own `free_up_hashes.json`;
AssetSyncCache's hashes don't say which version), needs Android 11 (`createTrashRequest`, launched
by RootView: it needs an Activity), and its trash keeps them 30 days. Both fetch the assets again
right before the deletion and drop any that changed, and drop the run after a Log Out or a change
of device. Settings live in UserDefaults (iOS, through SecretsStore) and the `otc_sync` prefs
(Android: not backed up), wiped at Log Out.
**Settings › Wi-Fi** (iOS `WifiSection.swift`: `WifiSection`, `WifiViewModel`, `enum WifiFlow` <->
Android `WifiSection()` in `ui/settings/SettingsView.kt`, `WifiViewModel` in `SettingsViewModels.kt`,
`object WifiFlow` in `ui/settings/WifiFlow.kt`; the rules and the device side are under `network`
above): `WifiFlow` holds the pure rules, tested with the web's case ids (`WifiFlowTests.swift` <->
`WifiFlowTest.kt`). "unknown_payload until the next sign-in" is `DeviceSupport.wifi` (reset by
`signedIn()`) on iOS and the sign-in count (`OTCConnection.signIns`) on Android. The password lives
in the view model only, is sealed with `GetPubKey` + `PwCrypto` and cleared once sealed; no
password AutoFill (iOS a plain `SecureField` with no content type; Android a platform `EditText`
with `importantForAutofill = no`, as every Compose field takes part in autofill) and Return/Done is
Connect; the network-name field has no capitalisation, correction or smart punctuation (iOS a
`UITextField` wrapper: SwiftUI's `TextField` can't turn smart quotes off). iOS keeps its wait in the
view model while the Settings tab lives; Android stops looking in the background or when Settings
is left and picks the wait up again on return, reopening the picker on the network tried after a
wrong password. `DeviceUnreachableView` shows the cable hint (`app.shell.unreachable_wifi_hint`)
under the device's words and before "still trying", except for a disabled account and a device
that can't reach its storage.
**A device without its storage** (issue #207; iOS `StorageWait` in `OTCConnection.swift` <-> Android
`net/StorageWait.kt`; `ErrorCodes.storageUnavailable` / `STORAGE_UNAVAILABLE`): `storage_unavailable`
on GetPubKey, Auth or any signed-in answer (the envelope's `error_code`, or an Ack's code) sets
`statusCode` to it and the card shows a disk, `app.shell.no_storage_title`, the device's words (else
`app.shell.no_storage`), no cable hint, and "Still trying…". It is no verdict on the password:
nothing is cleared and the auth-rejection gate isn't touched. For 30 s after each such answer a
connect fails at once with the device's words without dialling (`held`; error code 7 on iOS); the
device is asked again every 30 s, or at once at a wake (foreground, a network up); `invalidate()`
lifts the hold, Log Out ends the state, and any other answer (its key, the bridge's verdict) ends it.
Tests: `StorageUnavailableTests` (RetryTests.swift, a `DeviceStandIn` with a `reply`) <->
`StorageWaitTest.kt`.
**The first-run tour** (tour 2, 2026-10-28: iOS `Tour.swift` + `TourArt.swift` <-> Android `ui/Tour.kt` +
`ui/TourArt.kt`: `TourStep`, `TourTarget`, `TourRules`, `TourModel`, `TourTargets`, `TourView`/`TourLayer`;
tests `TourTests.swift` <-> `TourTest.kt`, cases T1-T19; the web's is under Frontend): a card RootView lays
over MainView (iOS `TourOverlay`, Android a `Box` with MainView's semantics cleared), so SyncChoice and every
sheet and dialog open above it, teaching one thing a step by showing it - welcome (the bottom bar, or the
menu from 600 pt/dp), search, dates (the scrubber's strip), select, viewer, people (only while
`FaceRecognition` is on), collections, files (View options: List/Grid/Storage, Sort by), upload (Upload,
Share/Download, the upload-only lock), social (heart, comment), post (+ / + New post, Phone/Synced,
caption, Publish), friends, backup (Settings › Sync Options, Wi-Fi only), users (Settings › Users, Add and
Request bridge access; only for the device's owner, `GetInstanceRole` - iOS
`UsersManagementViewModel.askIsPrimary`, Android `askIsPrimary`, asked as the tour opens, unknown =
left out), alerts (Alerts and Settings, Show the tour). Steps go by id (`TourRules.place`), so People and
Users can come or go mid-tour; texts `app.tour.*`, every quoted label an argument from its own key
(iOS `TourTexts`, Android `tourBody`). Each step shows its section (`TourRules.section`: MainView follows
`TourModel.step`, its own tab or page - never a menu's way in, which clears a search; narrow People,
Collections and Friends stay Images and Social, whose buttons are rung, as their sheets would cover the
card; never Alerts, which marks every alert read as it opens), and ending it - Skip, Done, Escape, Android
Back - goes back to where it opened (`tourFrom`). Where a step's controls are on screen
(`TourRules.targets`, marked with `.tourTarget` / `Modifier.tourTarget`: an iOS `.global` frame from
`onGeometryChange`, an Android `GlobalPositionAwareModifierNode` with the unclipped window frame - Settings
scrolls to rows out of sight; the iOS bottom bar is UIKit's, read from its `UITabBar`, `TabBarProbe`) the
dimmed layer leaves a hole with an ember ring (`TourRules.holes`: padded 6, merged when they touch); the
layer takes every touch, so the card is all there is to use. The card (`TourRules.cardFrame`) is a sheet at
the foot of a narrow window, or its top or middle when that covers fewer rings; from 600 it goes beside the
rings (the roomiest side, else the corner that covers least); its text scrolls when large. Settings scrolls
to what its steps ring (`settingsScroll`; an iOS Form only to a row's top, middle or foot, again as Status
and Users come in); narrow, the last step rings the tabs only (Show the tour can't scroll clear of a
top card). Every step has a small moving drawing (`TourArt`, a 320 x 176 stage scaled to the card: SwiftUI
shapes and Compose Canvas, each platform's own icons, labels from the real keys with samples
`app.tour.art.*` - `art.thing` stays English, as RAM++'s tags -, theme colours, gradient tiles and
outline faces, never a real person; a timer steps through each scene's states, one still state with
Reduce Motion / ANIMATOR_DURATION_SCALE 0; hidden from VoiceOver and TalkBack), so it teaches an empty
library too; a debug-only switch rings nothing for that check (iOS env `OTC_TOUR_NO_TARGETS=1`, Android
extra `--ez otc_tour_no_targets true`, debuggable builds only). It opens by itself once: signed in, MainView
up with no card, SyncChoice answered and the stored version below `TourRules.version` (2: phones that saw
the first tour see this one once; raise it only for a rewritten tour). `evaluate` writes the mark before
showing it; a store it can't read or write opens nothing. The mark is the phone's alone: iOS defaults suite
`cloud.off-the.OffTheCloud.tour` key `seenVersion` (outside the domain Log Out removes), Android prefs
`otc_tour` key `seen_version` (`commit`; not wiped at Log Out, not in the backup rules). Settings › Getting
started replays it from step 1 and leaves the mark; Log Out only closes it (`reset`). Card: Skip at the top,
Back (from step 2) and Next/Done in ember with --ember-ink text, side by side or stacked at large text;
iPad Escape/Return are Skip/Next; the card is modal for VoiceOver (escape is Skip) and a polite live region
on Android; it sends nothing that changes anything (only the reads its sections make).
**People fixes, Files' columns, wallpaper (2026-10-28).** With exactly one person in Images' filter
(tags may join), the selection bar and the viewer's ⋮/… menu add Not this person (asks first) and
Someone else… (a sheet of the other people, a name filter past 8; iOS `PersonMove.swift`, Android
`ui/gallery/PersonMove.kt`): `RemoveFromPerson`, the tiles leave at once, People reloads,
`unknown_payload` -> DeviceNeedsUpdate (`app.photos.person.needs_update`). Android: Use this face (the bar
with one photo picked, not a video - `coverPhoto` -, and the viewer's menu) and the header face's menu
(`CoverFace`: Upload a photo… through the phone's picker and `ProfilePhotoCropDialog` with `crop_title`/
`crop_hint`, a JPEG of at most 256 KB - `personCoverFits` -, Choose automatically while `cover_chosen`) send
`SetPersonCover` (`setPersonCover`, busy `PERSON_COVER`): `cover_done` and People listed again, else the
device's words or `cover_failed`, `unknown_payload` -> DeviceNeedsUpdate (`cover_needs_update`)
(`PhotoGalleryCoverTest.kt`; debug-only `PersonCoverPreviewActivity`, `--es device demo|old`). A person's face on iOS
(`PersonCover.swift`, `PersonCoverTests.swift`): Use this face (`NavIcon.useFace`, the face with a tick)
in that bar (one photo picked, not a video) and first in the viewer's menu sends `SetPersonCover` `hash`;
their face in `PersonHeader` is a Change face menu - Upload a photo… (PhotosPicker, then
`ProfilePhotoCropView` with `crop_title`/`crop_hint` and a 256 KB budget, `image`), Choose automatically
while `cover_chosen`, `cover_hint` as its last line. Done: `cover_done`, the pick cleared, People listed
again (a picture shows at once); refused: the device's words or `cover_failed`; `unknown_payload` ->
DeviceNeedsUpdate (`cover_needs_update`). Files' List view on a wide
window (the 600 dp/pt wide layout; iOS also needs `FilesColumns.fits`) has Name/Size/Created/Modified
headings, a port of web `filesColumns.ts` (iOS `FilesColumns.swift` + `FilesColumnsView.swift`, Android
`ui/files/FilesColumns.kt` + `FilesColumnsView.kt`): a heading tap is `pickSort`, an edge handle drags
and double-taps to reset, widths kept in iOS defaults `files.columns`, Android `otc_settings`
`files_columns`; narrow rows are unchanged. iOS alerts raised while the viewer is open show over it
(`ImageModal`), never closing it. A collection as wallpaper (`app.photos.wallpaper.*`) always starts
with the notice that copies are stored on the phone (stored_title in bold first, the count, the name),
at most 100 screen-sized photo copies: Android (`wallpaper/`: `WallpaperRules`, `WallpaperFetch`,
`Wallpaper`, `ui/gallery/WallpaperBox.kt`) keeps them in `filesDir/wallpaper/<collection>/`, prefs
`otc_wallpaper`, a WorkManager download job (unmetered under Wi-Fi only) and a periodic one that sets
lock/home/both with WallpaperManager; Stop, deleting the collection, Log Out and another device delete
the copies and leave the last photo up. iOS can't set wallpapers (`WallpaperView.swift`): the lock screen gets a
Photos album "Off The Cloud · <name>" for Photo Shuffle (full Photos access; every asset made is in
`SavedToPhone`, so `PhotoSync.leftOut` keeps it out of the sync and Free Up Space; Stop deletes them in one
change, iOS asks - only with full access to Photos: without it nothing can be seen or deleted, so the
album is kept, not forgotten, and the screen says what Stop needs; an album deleted in Photos keeps its
copies the app's to delete; Log Out waits for an album change under way and makes nothing after it),
and the home screen the `OffTheCloudWidgetExtension` target (`cloud.off-the.OffTheCloud.Widget`,
App Group `group.cloud.off-the.OffTheCloud`, both registered 2026-10-28; copies <= 1200 px in the group
container, its `Widget` folder kept out of iCloud and computer backups (`keepOutOfBackups`: the group's
container is backed up like the app's own), `AppIntentConfiguration` over the collections with copies, 6 hourly entries per timeline - a
24-entry one is refused as too large -, its texts from the app's language through the group).
**Settings sections the device decides** (2026-10-29: Users, Device version, Wi-Fi, Tailscale Funnel,
Bridge and Account, Troubleshooting): until the device has answered for one - its role and, for the
owner, the section's first status (the users list, the update check, Wi-Fi's status, Funnel's) -
Settings shows the section's header and a spinner with `common.loading` (iOS `LoadingSection` and the
`DeviceSection` models SettingsView owns and asks in `loadDeviceSections`; Android `LoadingSection` and
`AskDevice`); one the device says it doesn't have takes no room at all (iOS used to leave an empty row
per section, a blank box on a slow connection). A request with no answer leaves a section as it was:
the unanswered ones are asked again after 5 s, doubling to a minute, while Settings shows, and all of
them whenever Settings shows and at each wake.
**Places, the map in Images** (2026-10-29; iOS `Places.swift`; Android `Places.kt`, `PlacesModel.kt`,
`PlacesMap.kt` - its own paragraph below; the shared texts are `app.settings.places.*` and
`app.photos.places.*`). Only while Places is on - the user's switch, on by default since release 170, as the device keeps where each photo
was taken unencrypted only then (`PlacesSupport`: `GetSettings`' `places_enabled`, absent on a device
before Places = off, asked on every connection from `OTCConnection.connected`, the last answer kept in
defaults `placesOn` so it doesn't come and go at launch, reset at Log Out; Settings' Places section
after People - "Places" switch, `app.settings.places.footer` consent text, only when the device's
Settings carry the field - and a map told `places_off` or `unknown_payload` set it at once; off hides
it and drops the area filter). Just turned on, the device reads positions in the background
(`PhotoPlaces.reading`): the map's note says "Finding where your photos were taken…", those answers
are kept 15 s (`PlacesCache.readingTTL`) and the screen's tiles are asked again once they age
(`askAgainWhileReading`). The wide
layout's menu has Places after Collections (`AppSection.places`, a page), a narrow window a grid/map
switch in Images' search row (`vm.showMap`; MainView moves one to the other as the window changes).
The map shows what Images would show (`PlacesFilter`: tags, people, collection, videos always), on
Apple's map (SwiftUI `Map`, muted, no points of interest, pan and zoom only), opening where it was
left for the same search or framed on `located_area` (a single spot about a town across; the world
with "No photos with a location yet" when none has one). Photos all over the world open on the
world, as the web's (`PlaceTiles.opening`: when framing them takes more than 2^-0.25 of the world's
width, the world centred at 25° N on zoom 0's biggest place, else 10° E): the smallest area holding
photos from Tokyo to Mexico City runs across the Pacific, and framing it cut Europe in two at the
edges. Android's map must do the same. `PlacesModel` (one for the session,
`PhotoGalleryVM.places`) asks only while the map shows, once the camera rests 200 ms, for the screen's tiles and one around
them at `PlaceTiles.zoom` (log2(width·360/(256·lonSpan)) rounded down, 0-20) - 64 a request, the
extent first (zoom 0, the world's tile), the next zoom's after 600 ms more -, keeps answers per
(filter, zoom, x, y) for 5 minutes or until the library changes (`PlacesCache`; a delete, a folder
kept out, the map shown again after a minute), draws kept tiles at once and the pins drawn before for
a tile on its way, retries with the pages' backoff (a "Couldn't load the photos on the map" banner
with Try again meanwhile), and merges places closer than 54 points on the screen (`PinMerge`: the
biggest first, drawn at the member nearest their weighted mean, the newest cover and its id, so a pin keeps its place across zooms; again until no
two are that close, as a merged pin moves). A pin
(`PlacePinView`) is the cover's small thumbnail (`GridThumbLoader` at 48 pt, by hash, the pins on the
screen first) in a white rounded frame with an ember count, fading in (not with Reduce Motion);
VoiceOver reads a tile's label for one photo, `app.photos.places.pin` for a group. A tap
(`PlaceTap`): one photo opens in the viewer (`ImageModal` on a fixed list, as Files'); a group zooms to
fit its area, at least twice as close; from zoom 16, or photos within 25 m, it opens in Images with
`SearchPhotos.area` and the month counts' `area` (`vm.activeArea`, the "Map area" chip: tap for the
map where it was, × for every place; and "Back to the map" at the top of the grid, below). Tests:
`PlacesTests.swift`.
**Places on Android** (2026-10-29, owner: "let's use these maps in the android app too so we don't depend
on Google"): the web's map, MapLibre Native (`org.maplibre.gl:android-sdk-opengl`, pinned - the plain
`android-sdk` is the Vulkan build, which Play would offer only to phones with Vulkan 1.0) on OpenFreeMap,
no key and no Google services. Its styles ship in `assets/places/` (`style-dark.json`, the web's
recolouring of OpenFreeMap's dark, and `style-light.json`, Positron the same way - the theme picks;
`app/android/scripts/places-style.mjs` writes both from OpenFreeMap's, its colours the web's
`cColours`: change them together), with no sprite, place names `name:<the app's language>`; tiles,
TileJSON and glyphs come from tiles.openfreemap.org, nothing asks for the phone's position (the
manifest removes MapLibre's location permissions: `tools:node`). The same rules as iOS's, ported case for
case (`PlacesTest.kt`, `PlacesModelTest.kt`, on a square world 1 across - `WorldPoint`/`WorldRect` for
MKMapPoint): `PlacesSupport` (Settings' `places_enabled`, from the same `GetSettings` answer as
`FaceRecognition.watch`, from Settings' load, and off on `places_off`/`unknown_payload`; kept in
`otc_device_state`, reset at Log Out), Settings' Places section after People (only when the device's
Settings carry the field), `PlacesModel` (one for the session, `PhotoGalleryViewModel.places`: extent
first, the screen's tiles and one around 200 ms after the camera rests, 64 a request, the next zoom's
600 ms later, `PlacesCache` 5 minutes or 15 s while reading, `PinMerge` at 54 dp, retries 1 s doubling
to 10 s, thumbnails by hash from `ThumbStore` else `GridThumbFetcher`), `PlaceTap` and
`PlaceTiles.opening`. `Section.Places` is the wide menu's page after Collections (`menuGroups(faces,
places)`; Images with a map area marks it); on a narrow window the same section shows the map in the
grid's place, with the switch in Images' search row and Back to the grid. A group opened in Images is
`State.activeArea` (`SearchPhotos.area`, the month counts, the upward pages; `bucketsKeyOf` keys it)
and the "Map area" chip in the search field (`onShowMap`: back to the map, × `leaveArea`; hidden
while the map shows). The pins are Compose over the map (`PlacesMap.kt`): laid out - not composed
again - from the camera's own numbers as it moves (`MapCamera`, MapLibre's zoom counting 512-point
tiles, the device's 256: one more), in a TextureView map so they move with it; they take no touches
(the map keeps every gesture), a tap is the map's click against the pins' frames (`pinAt`), and
TalkBack reaches each one by its label and action. One photo opens in a viewer of its own
(`showFiles`), as Files opens one.
**On this day on Android** (2026-10-30; `data/MemoriesSupport.kt`, `ui/gallery/MemoriesModel.kt`,
`ui/gallery/Memories.kt`; the device side and protocol are under Places' device notes; the shared texts
`app.photos.memories.*`, `app.settings.memories.*`; iOS must match): only while the device says it is on
(`MemoriesSupport`: `Settings.memories_enabled`, absent = a device before it = hidden, from the same
`GetSettings` answer as `FaceRecognition.watch` and Settings' load, kept in `otc_device_state`, reset at
Log Out; `memories_off`/`unknown_payload` set it off). Settings' On this day section after Places
(heading "Memories", switch `app.settings.memories.toggle`, `SetMemoriesEnabled`; "Show hidden memories
again (N)" while today's day answer's `hidden_count` is above 0, `HideMemory show_all`, then
`MemoriesSupport.changed()` makes Images ask again). `MemoriesModel` (one for the session,
`PhotoGalleryViewModel.memories`) asks while the strip may show (the grid over the whole library: no
search chip, person, collection or map area): day, week and month one after the other for the phone's
date, IANA zone and offset (`MemoriesDay`), again at the next midnight, every 2 minutes while an answer
says `measuring`, after a hide or Settings' change; a failure 1 s doubling to 10 s, at once on a Wake.
`MemoriesRules.period`: the choice picked this session while it has memories, else the first with any,
day -> week -> month, decided only once the earlier ones answered; a choice without memories can't be
picked; no memory in any = no strip. The strip is a LazyColumn item above the headers (`head:memories`,
counted in `headerCount`; landing while the grid is at its top it scrolls into view): the title by the
choice, Day / Week / Month chips, a card per memory (cover by hash from the thumbnail cache, else
`GetThumbnails` by path; "N years ago", the place). A card opens `MemoryViewer`: a full-screen dialog,
4 s a photo timed by frames (animations off drop the cross-fade, never the advance), a tap on the left
third goes back, elsewhere on, holding pauses, after a memory's last photo the strip's next memory
(`MemoriesRules.neighbour`, `onMove`), closed after the last, back from a first photo the memory
before, Pause/Resume, progress segments, the line
(`memoryDays`: "30 Oct 2023", "27 Oct - 2 Nov 2023" by `week_in_year`, "October 2023") and the named
people (`peopleTitle`), the big thumbnail (`GetThumbnails` without small, 8 kept) over the tile; starts
paused with TalkBack, whose sides are buttons. Its menu hides the photo (the viewer stays), the memory,
the photo's day in the phone's zone, or a named person (`hideablePeople`: a name exactly one person in
People has; the memory carries names, HideMemory a person id) - the last three close it; a photo or a
memory leaves the strip at once (`withoutHidden`), then everything is asked again; the notice
(`app.photos.memories.hidden`, Undo = HideMemory `show`, 6 s, a polite live region) shows in the viewer
or at the grid's foot; a refusal (the demo) shows the device's words, else
`app.photos.memories.hide_failed`. See all is a grid of the memory's photos opening a viewer of its own
(`showFiles`). A debug-only `MemoriesPreviewActivity` (`src/debug`, `adb shell am start -n
cloud.offthe.otc/.MemoriesPreviewActivity [--ez open true]`) draws both on sample data. Tests:
`MemoriesModelTest.kt`.
**Smart search on Android** (beta, 2026-10-30; `data/SmartSearchSupport.kt`, `TopSearch.kt`,
`PhotoGalleryViewModel.kt`, Settings' `SmartSearchSection`; the device side under its own notes; texts
`app.settings.smart_search.*`, `app.photos.smart_search.*`; iOS and the web must match): only while
the device says it is on (`SmartSearchSupport`: `Settings.smart_search_enabled`, absent = a device before
it = off, from the same `GetSettings` answer as `FaceRecognition.watch` and Settings' load, kept in
`otc_device_state`, reset at Log Out; a search told `smart_search_off` sets it off). Settings' section
after On this day (only when the device's Settings carry the field): "Smart search" with a Beta badge,
the footer; on at once (the device starts describing), off asks first (`off_title`/`off_text`, Turn Off:
the device deletes what it learned); `smart_search_unavailable` and the demo's refusal show the
device's words. The search: typing works as before (tags and people become chips); while on, the panel
has "Search photos for "x"" (`SearchOption.Smart`, a sparkle) - first, and what the Search key takes,
unless a thing or a person is named exactly (then after them, the exact one taken); picked, it is
`PhotoGalleryViewModel.searchText` (`State.smartText`, a chip of its own after the tags, x =
`clearText`; the field's clear takes it too, and so does Images in the wide menu). Pages send
`SearchPhotos.text` only while `SmartSearchSupport` is on, with the chips, people, collection and area
as filters, never `before`/`newer_than`; the order is the device's (`dateOrdered` false: no month
titles, buckets or scrubber, no upward search, no person header, no memories). `smart_search_reading`
shows "Your device is still getting to know your photos…" over the results (`head:smart`).
`smart_search_off` (also turning `SmartSearchSupport` off) and `smart_search_unavailable` drop the text,
say the device's words (else `app.photos.smart_search.failed`) in the gallery's alert, and search the
chips again by date. The model on demand (release 152, iOS's names and rules): `SmartSearchSupport.model`
(`SmartSearchModelState` Ready / Missing / Downloading(percent, problem) from the same `GetSettings`
answers, `deviceSaid`; absent = an older device = Ready; not kept, Ready again at Log Out). The row and
the Search key need `searchable` (on and Ready). Settings shows under the footer `download_note`
(Missing) or a bar plus `downloading` / the device's `problem` (Downloading), asks `GetSettings`
(`refreshSmartSearch`, Smart search's part only) every 3 s while on, Downloading and in front, and once
after the switch moves while the model isn't Ready; turned on where the model isn't, it is Downloading(0) at once
(`modelRequested`). TalkBack: the bar's own percentage; a 1 dp polite live region only for start,
problem and done (`downloading` at 100%), emptied after 5 s (`SmartSearchModelState.announcement`, the
web's). A search told `smart_search_downloading` drops the text as `smart_search_unavailable` does,
keeps the switch on, sets Downloading and asks where the model is. Images learns that it is ready
from those answers (sign-in, Settings) and when its search field gets the focus while the switch is on
and the model isn't Ready (`searchFocused`: one `GetSettings`, at most every 5 s, `asksOnFocus`) - no
poll outside Settings. On by default from release 152 (off in 151): the switch is always the device's
answer, never assumed; `download_note` shows only while the switch is off (on with the model missing,
the device is about to fetch it). A debug-only
`SmartSearchPreviewActivity` (`--es show settings` for the section, `--es model
missing|downloading|problem|ready`; turned on from missing it runs a pretend download) draws both on
sample data. Tests: `PhotoGallerySmartSearchTest.kt`, `SmartSearchModelTest.kt`.
**Background work on Android** (release 152, `ui/settings/BackgroundWork.kt`; iOS's `BackgroundWork`;
texts `app.settings.background.*`): in Settings' In progress section (`InProgressSection`, owner
2026-11-07: "should be in the same section" as the uploads; heading `app.settings.progress.title`),
right after Status and Storage: this phone's uploads first (the old Uploads section's rows, shown while
`BackgroundWork.showsUploads` - sending, photos waiting, or a halt), then the device's rows, shown only
while `Status.background_tasks` isn't empty (an older device sends none), with
`app.settings.background.footer` only when there are some: a row per task (keyed by `kind`)
with the device's `text`, a `LinearProgressIndicator` (indeterminate while `total` is 0, which shows
no count) and `count` ("4,210 of 12,800", the app's number format), each row one TalkBack element. It
reads the Status answers Settings already asks for every 5 s while in front (`StatusViewModel`), so it
has no poll of its own. A debug-only `BackgroundWorkPreviewActivity` draws it on a sample upload and
tasks. Tests: `BackgroundWorkTest.kt`.
**Sort by and Back to the map on Android** (2026-10-30, the owner's request after user feedback;
`ui/gallery/PageSort.kt`, `showsAreaBack` in `Places.kt`, `AreaBackButton` in `PhotoGalleryView.kt`; iOS
`PeopleSort`/`CollectionsSort`/`AreaBackButton`, the same rules; texts `app.photos.sort_by`,
`app.photos.people.sort_*`, `app.photos.collections.sort_*`, `app.photos.places.back_to_map`): People
and Collections have a Sort by menu (`SortByMenu`, with two or more: the sort icon in People's narrow
bar and the Collections sheet's header, the icon and the order in words in a row atop the wide pages;
heading "Sort by", a check on the order in use; TalkBack hears "Sort by" and the order). People: Most
photos (the default, the page's order as before, `peopleOrder`) or Name; Collections: Newest first (the
default, the device's order) or Name. Name is A to Z by Files' `NameCollator` (ICU, primary strength,
the app's language, numbers by value), the nameless last and equal names in the default order
(`PageSort.byName`). Kept in `otc_settings` as `people_sort` ("photos"/"name") and `collections_sort`
("newest"/"name"), like `files_view_mode`: backed up, wiped at Log Out, read as a page shows; the "Add
to collection" picker and Someone else… keep the default order. Images over a place opened from the
map (`State.activeArea`, Places on, the grid showing) starts with `AreaBackButton` (`head:area`, in
`headerCount`; outlined, 48 dp, an arrow and "Back to the map") on narrow and wide windows, doing what
the "Map area" chip's tap does (`onPage(GalleryPage.PLACES)`: the map where it was); the chip stays. A
debug-only `PageSortPreviewActivity` draws them on sample data. Tests: `PageSortTest.kt`.
**Favourites on Android** (2026-10-31; the device side under "Image groups are called collections"; iOS must
match): the viewer (`ImageModal`: Images, People, Collections, Places, On this day, Files) has a heart between
Share and Save (`FavouriteButton`, a `Role.Switch` named by `app.photos.viewer.favourite_add`/`_remove`, ember
when full). Each photo shown asks `GetFavourites` for it and its neighbours in one request (`askFavourites`;
no answer: again with the pages' backoff, 5 tries at most, `FAVOURITES_TRIES`); until the open one's answer is
in (`State.favourites`, by path, forgotten as the viewer closes) its room stays empty. A tap draws at once
(every tile of the photo, `samePhoto`), with a ToggleOn haptic and a pop as it fills (no pop with animations
off), and `SetFavourite` goes one per path at a time (`sendFavourite`: the device ends with the last tap);
refused or unanswered, the heart goes back and a `Toast` in the viewer says the device's words or
`favourite_failed` (`favouriteRefusal`). `unknown_payload`: no heart, nor its room, until the next sign-in.
Inside Favourites a photo taken out stays in the viewer and leaves the grid as it closes (`dropUnfavourited`);
the collections are listed again once it has closed. Favourites is first in either Sort by
(`PageSort.collections`), has no Rename or Delete, and an empty one's cover is a heart (card and sheet). A
debug-only `FavouritesPreviewActivity` (`--es device demo|old|silent`, `--es show collections`) draws them on
sample data. Tests: `PhotoGalleryFavouritesTest.kt`, `PageSortTest.kt`.
**A photo's faces on Android** (2026-11-01, from a user; the device side under `face_recognition` above;
`ui/gallery/PhotoFacesInfo.kt`; iOS must match): every viewer's Info (`ImageModal`: Images, Files, Places, a
memory's See all) asks `GetPhotoFaces` for the photo or video open as it shows (`openInfo` -> `askPhotoFaces`;
a device before `adds_people` answers a video with no one; `unknown_payload`: nothing until the next sign-in,
quietly; a refusal or no answer leaves the rest of Info) and lists them after the details under People
(`app.shell.section_people`), left to right, then the people added by hand (`by_hand`) in the order added:
the crop at 36 dp (a blank circle without one), the name or `app.shell.search_unnamed`, and a 48 dp ×
named `app.shell.not_this_person`. Nothing while the device says `face_recognition` false or
`FaceRecognition` is off. A face opens that person's photos: the viewer closes, `PersonNav` (the person
asked for, like `FilesNav`) makes MainView show Images, and its gallery takes it (`showPerson`, as from
People; a memory's grid closes). The × asks Not this person's question (`notThisPersonTitle(person, 1)`,
`remove_text`, Remove), then sends `RemoveFromPerson{person_id, [PhotoFaces.hash]}` (`removeInfoFace`,
busy `NOT_PERSON`): the note `person.removed`/`removed_unnamed` (a video's `removed_video`/
`removed_video_unnamed`, `infoTakenOffNote`), else `remove_failed`; People is listed
again (not by a viewer of its own). Among that person's photos (`selectedPeople` holds them) the photo
leaves the grid and the viewer moves on, as after the viewer's Not this person (Info closes with the move);
otherwise Info drops their faces at once and asks the device again. The map's and a memory's viewers show
their notes (`ViewerAlert`). **People added by hand** (owner, 2026-11-09; `AddToPerson`): a `by_hand` row looks
and opens as a face's, its crop their People face; its × (`app.shell.search_remove`/`search_remove_person`)
sends the same `RemoveFromPerson` at once, no question (it is about face recognition; re-adding is a tap). People
shows with someone in it or, where `adds_people` and a hash (`PhotoFacesInfo.adds`), with Add a person alone
at its end (`app.photos.viewer.face_add`, Material `PersonAdd` in a 36 dp circle, 48 dp row): `openInfoAdd`
(`State.infoAdd`, People asked again) opens `AddPersonSheet` over Info - `PersonPickSheet`, the sheet
Someone else… now shares (`PersonMove.kt`: rows, a `face_add_find` field past 8 offered - `wantsNameFilter`,
iOS `filterAbove`; Someone else… had it from 8 -, `withName`), titled `face_add_title_photo`/`_video`,
`peopleToAdd` (People's order minus everyone in it), `face_add_none` (nobody found) / `face_add_all`,
`face_add_no_match`, a bar while busy, Cancel. A pick (`addInfoPerson`, busy `ADD_PERSON`) sends
`AddToPerson{person_id, [hash]}`: taken, the sheet closes, People is listed again (not by a viewer of its own)
and Info asks `GetPhotoFaces` again - only if the viewer is still on that item (`open` clears `infoAdd`; the
ask's counter drops an old answer); refused, the sheet says the device's words or `face_add_failed`
(`addToPersonRefusal`; `unknown_payload` too), or the note does if the sheet was closed meanwhile.
Debug-only `PhotoFacesPreviewActivity` (`--es device demo|noadd|old|off`; a video and a dozen people). Tests:
`PhotoGalleryFacesTest.kt`.
**A collection's Rating on Android** (release 163; owner, 2026-11-02; the contract is `proto/messages.proto`'s
"Rating (release 163)"; `ui/gallery/Rating.kt` (rules) + `RatingViews.kt`; texts `app.photos.rating.*`,
`app.files.gallery_rating*`, `app.settings.links.rating` - the iOS app's keys; iOS must match). The open collection's header has a
star (`RatingToggle`: outlined off, ember filled on, tooltip `title`, read out `on`/`off`; labelled on a wide
window) sending `SetImageGroupRating` (`toggleRating`, busy `ratingBusy`): on, the photos are asked again with
their `File.rating` and the first time from this phone (`RatingHint`, `otc_settings` `rating_hint_seen`) a note
says `turned_on`; off, Sort by and Show go back to Date / All photos; `unknown_payload` -> DeviceNeedsUpdate
(`needs_update`), a refusal the device's words or `toggle_failed`. A listing whose `rating` differs from the open
collection's (another app, a gallery shared with rating) does the same, unless the switch was turned here after
that list was asked (`ratingSetAt`). With it on the header has `RatingControls`: Sort by (`RatingSort` Date /
Rating -> `by_rating`) and Show (`RatingShow`: All photos, 5 stars, 4/3/2 or more, Rated, Not rated, With a
comment -> `RatingFilter`; filled while not All photos); either one makes the grid ranked (`State.dateOrdered`
false: no months, buckets or scrubber); a Show that leaves nothing is `EmptyKind.RATING` (`no_match`, Show all
photos, `show_everything`). Both reset when a collection is opened. Ratings live in `State.ratings` by path
(pages, kept answers), apart from `items`; tiles show `RatingBadge` ("★ 4", a bubble for a comment; read out
after the date, `comment`). The viewer (`viewerRates`: a rated collection, never a fixed list's - Files, the
map, a memory) has, in portrait, five stars (`RatingStars`, radio buttons) and a bubble opening `RatingSheet`
(`comment_placeholder` / `comment`), with `from_link` on the line under them (always kept, so the photo doesn't move); in landscape a star with the number in
the side column opening the sheet (stars, the note, the comment box up to 2000 characters, Save while changed). A
tap gives that many stars, the photo's own number clears them (`RatingRules.stars`), sent at once with the box's
comment; `SetPhotoRating` one at a time per photo and collection (`ratingKey`), shown at once and put back on a
refusal (`ratingNote`: the device's words - `rating_off` also lists the collections again, `not_in_collection` -
else `save_failed`). A comment not saved is saved as the sheet closes, the viewer moves on or closes
(`leaveRatingDraft`); a ranked grid is asked again as the viewer closes once the sends are done. Share as gallery
from a collection has the switch `gallery_rating` (`CreateSharedGallery.rating`, off by default; the collections
are listed again after the flow), and Settings › Shared links shows `links.rating`. Alerts lists a
`NotificationRating` (`RatingRow`: a star in the avatar's place, the device's title in bold, its details, the
time, as `UpdateRow`); a tap (`NotificationsModel.handleTap` -> `ratingCollection`: `ListImageGroups`, the
alert's `group_id`) sets `collectionRequested`, MainView shows Images and its gallery opens the collection
(`openGroup`); deleted since, Alerts' notice says `app.social.alerts.rating_gone`, no list `rating_open_failed`
(or the device's words). Debug-only `RatingPreviewActivity` (`--ez off true`, `--es device demo|old`). Tests: `RatingTest.kt`,
`PhotoGalleryRatingTest.kt`, `AlertsTest.kt`.
**A photo's faces on iOS** (2026-11-01, Android's rules above; `PhotoFaces.swift`, tests
`PhotoFacesTests.swift`): `FileInfoView` shows People after the details and the map, once they have
loaded (`loadInfoFaces` when Info opens, for a video too; not for a photo only on the phone; any error,
`unknown_payload` included, hides it quietly; an answer for another photo is dropped). The crop is 36 pt
(growing with the text size up to 72), the × grey with a 44 pt target, a spinner while it is on its way
and the others disabled meanwhile. A face opens that person (`showPerson` in Images; from Files, the map or
a memory, `PersonNav` makes MainView show Images on them and closes an open memory). The ×: Not this
person's question for one photo, `RemoveFromPerson`; among that person's photos the photo leaves the grid
(`leavePerson`, shared with the viewer's Not this person) and Info closes with the result over the viewer;
elsewhere the face leaves the list at once and it is asked for again. Alerts raised while Info is open show
over Info (nothing the viewer presents can cover its own sheet). People added by hand (2026-11-09,
`AddToPerson`; the rules are `PhotoFacesState`'s): with `PhotoFaces.adds_people` (and face recognition on, a
hash) People shows even empty and ends with Add a person (`face_add`, `person.badge.plus`, 44 pt), opening
`PersonAddSheet` over Info (`openFaceAdd`, People asked again): `face_add_title_photo`/`_video`, People's
list minus everyone in it (`addChoices`/`addList`, `PersonMove.choices` with a Set) in Someone else…'s rows
(`PersonPickRow`, shared), `NameFilter` with `face_add_find` past 8, `face_add_none`/`_all`/`_no_match`, a
spinner on the row picked and the others disabled, a refusal above the list (`addFailure`: the device's
words, else `face_add_failed`; `unknown_payload` and the bridge's unreachable too), Cancel. `addPerson`: on
the Ack the sheet closes, People is asked again and so is the item's list unless the viewer moved on
(`faceAddAsk`; `clearInfoFaces` and `openInfo` close the sheet). A `by_hand` row is a face's; its × (named
`app.shell.search_remove`/`search_remove_person`) skips the question and goes through `removeFace`.
**Sort by and Back to the map on iOS** (2026-10-30, the owner's request after user feedback;
`SortBy.swift`, `AreaBackButton` in `Places.swift`, tests `SortByTests.swift`; Android's rules above,
case for case): People and Collections have a Sort by menu (`SortByMenu`: the sort icon at the
leading end of the narrow sheets' bars - People's sheet, `CollectionsSheet` -, the icon and the order
in words atop the wide pages, `SortByHeader`, 44 pt high; a "Sort by" section with a check on the
order in use; VoiceOver hears "Sort by" and the order). People: Most photos (`PeopleSort.photos`, the
default, the page's order as before, `PeopleOrder.shown`) or Name; Collections: Newest first
(`CollectionsSort.newest`, the default, the device's order) or Name. Name (`NameOrder`) is A to Z by
`FilesSort.compareNames` in the app's language (case and accents ignored, numbers by value), names
trimmed, the nameless (Untitled) last and equal names in the device's order. Kept in UserDefaults as
`people.sort` ("photos"/"name") and `collections.sort` ("newest"/"name"), like `files.viewMode`:
wiped at Log Out with the app's defaults. Another order scrolls back to the page's top. The drag
selection, the merge sheet and the tour's first face follow the order shown; "Add to collection"
keeps the device's order. Images over a group opened from the map starts with `AreaBackButton`
(`AreaBackButton.shown(area:mapShown:)`, the chip's rule: `vm.activeArea` set and the grid showing;
ember outline, an arrow - `arrow.backward`, mirrored right to left - and "Back to the map", 44 pt
high, read as a button by its words), above a person's header, on both layouts:
it sets `vm.showMap` as the chip does - the narrow window's map in the grid's place, the wide one's
Places page (MainView) - the map where it was; the chip stays.
**Favourites on iOS** (2026-10-31; `Favourites.swift`, tests `FavouritesTests.swift` and `SortByTests.swift`;
Android's rules, case for case): `CollectionsSort.ordered` puts the `favourites` group first in both
orders; its chip has no Rename or Delete, and an empty one shows a heart for its cover. Every
`ImageModal` (Images, Files, Places, a memory's See all) has `FavouriteButton` after Share for the
device's own items (`FavouriteAsk.eligible`: never a photo only on this phone). `FavouritesModel` (one
for the session, by path; a heart changes every copy it knows by hash) asks `GetFavourites` for the
open item and its neighbours at each `open(index:)`, leaves the heart's place empty until that
answer, shows a tap at once and sends `SetFavourite` one at a time per path (a tap meanwhile goes
after the answer; answers read before a tap are dropped by `stamps`), puts it back on a refusal with
the device's words or `favourite_failed` over the viewer, a light haptic and a bounce (none with
Reduce Motion) as it fills; `unknown_payload` hides it until `OTCConnection.connected` (another
endpoint forgets the answers, Log Out resets). In an open Favourites an emptied heart keeps its photo
in the viewer and `closeModal` drops it from the grid (`dropUnfavourited`, as Remove from collection);
a heart from another viewer asks for the grid again (`FavouriteChange.by`).
**Rating on iOS** (2026-11-02, release 163's protocol; `Rating.swift`, tests `RatingTests.swift`; texts
`app.photos.rating.*`, `app.files.gallery_rating*`, `app.settings.links.rating`, shared with Android): the
open collection's chip has a star after Share (`toggleRating`, SetImageGroupRating; filled while on,
"Rating on"/"Rating off" to VoiceOver; done, its grid again - the listing carries `File.rating` only while
on - from Date and All photos; the first time ever, `RatingToastView`'s one-line note, `rating.introduced`
in UserDefaults; `unknown_payload` -> DeviceNeedsUpdate `needs_update`; refused, the device's words or
`toggle_failed`). While on, a row under the chip (`RatingControls`): `SortByMenu` Date / Rating
(`by_rating`) and `RatingShowMenu` (All photos, 5, 4, 3, 2 stars or more, Rated = min 1, Not rated,
With a comment -> `RatingFilter`; ember while not All photos); either one not the default makes the grid
`ranked` (`ratingRanked`: no months, buckets or scrubber); both go back to the defaults whenever a
collection opens or Images leaves it; none matching is `EmptyKind.ratingNoMatch` with Show all photos.
Tiles show `RatingBadge` ("★ 4", a speech bubble for a comment; VoiceOver: the stars as the tile's value,
the comment as more content). The viewer (`viewerRates`: never a fixed list - Files, the map, a memory)
has a Rate button after the heart (the stars and a bubble) that opens `RatingPanel` (`ratingPanelOpen`,
kept while swiping): under the photo in portrait - the buttons' row gives way while the comment is typed
-, a 320 pt column beside it in landscape; five stars (`tapStar`: the current one clears; one adjustable
VoiceOver element), "From the shared link" when `from_link`, the comment box (`RatingDraft`, outside the
view model so typing doesn't redraw the grid; at most 2000 scalars, `RatingRules.clip`) and Save (enabled
once `changed`). Stars save at once with the comment as the box has it (the web's and Android's rule: the
box is saved with them, so Save goes grey); the comment with Save, or by itself when
the viewer moves to another photo (`ratingOpened`) or closes (`ratingViewerClosed`). Every change shows at
once and goes through `ratingQueue`, one at a time, in order; refused, the photo shows what the device
holds (`ratingHeld`) and the device's words or `save_failed`; `rating_off` also turns the collection's
Rating off. Sorted or filtered, the grid is asked again as the viewer closes. Share as Gallery from a
collection has "Let people rate the photos" (`offersRating`, off by default, `CreateSharedGallery.rating`);
the link made, `.otcCollectionRatingChanged` lists the collections again, and a changed `rating` in that
list reloads the open grid (`ratingTurned`). Settings › Shared links says "Rating on" for such a link.
Alerts lists a Rating alert (`NotificationRating`, `ratingRow`) with a star for the avatar, the device's
title in bold, its details and the time; a tap (`handleTap` -> `DeepLink.collection(id:)`, MainView) lists
the collections and opens `Notification.group_id` in Images (`openGroup(id:)`, the grid, not the map), or
says in Alerts `app.social.alerts.rating_gone` (deleted since) or `rating_open_failed` (no list), Android's.
**On this day on iOS** (2026-10-30, `Memories.swift`, `MemoriesTests.swift`; Android's rules above,
case for case): `MemoriesModel` (one for the session) knows the switch from `GetSettings`'
`memories_enabled` (PlacesSupport's connection answer and Settings' load; absent = a device before
it, nothing shown) and asks `GetMemories` for day, week and month one after the other with the
phone's date, IANA zone and UTC offset (`MemoryDates.request`; Foundation calls UTC "GMT") - again
at midnight (`NSCalendarDayChanged`, back in front on another day), every 2 minutes while an answer
says `measuring` (`whileMeasuring`), after a hide and Settings' change; a failure again 1 s doubling
to 10 s (`PageRetry.delay`), at once on a wake; `memories_off` turns it off, `unknown_payload`
forgets it. The strip (`MemoriesStrip`) is the grid's first item over the whole library only
(`PhotoGalleryView.showsMemories`: no chips, people, collection or area), titled by the period shown
(`MemoryChoice.shown`: the one picked this session while it has memories, else day, week, month -
empty ones dimmed and disabled; none at all, or measuring with nothing, no strip), with a 120 x 160
card per memory (cover from `GridThumbLoader` at 160 pt by hash, "N years ago", the place). A card
opens `MemoryViewer` full screen: the big thumbnails by path (`MemoryPhotos`, GetThumbnails without
small_thumbnails, the cached tile meanwhile), 4 s a photo, the left third back and elsewhere on, a
press held, Pause/Resume, the menu or See all pause it (See all leaves it paused), segments on top,
the line (`MemoryText.header`: "3 years ago · 30 Oct 2023 · Asturias", the days as Android's
`memoryDays`) and the named people, a swipe down closes; after a memory's last photo the strip's
next memory (`MemoryStories`, `MemoryStep`), closed after the last, back from a first photo the
memory before, Pause held across them; with VoiceOver it
starts paused and its sides are buttons ("Previous photo", "Photo 2 of 6. Next photo"). See all is a
grid opening `ImageModal` on a fixed list. The menu hides the photo (it leaves the slideshow, the
viewer stays), the memory, the photo's day (its date in the phone's zone) or a named person - only
a name exactly one person in Images' people list has (`MemoryPeopleMatch`: `Memory.people` are
names) - the last three close it, as a memory gone from the answers does (hidden on another app); a
photo or a memory leaves the strip at once (`MemoryHiding.without`), then everything is asked again,
and `MemoryToastView` says "Hidden from your memories" with Undo (HideMemory `show`) for 6 s, read
out politely; a refusal (the demo) shows the device's words, no answer `hide_failed`. Settings ›
Memories after Places: the switch (`toggleMemories`) and "Show hidden memories again (N)"
(`show_all`) while `hidden_count` is above 0. Log Out resets it.

**Smart search on iOS** (beta, 2026-10-30; `SmartSearch.swift`, `SmartSearchTests.swift`; the device
side under its own paragraph above; texts `app.settings.smart_search.*`, `app.photos.smart_search.*`,
shared with Android, whose rules these are, case for case): `SmartSearchSupport` knows the switch from
`GetSettings`' `smart_search_enabled` (PlacesSupport's connection answer and Settings' load; absent = a
device before it: no switch, no free-text search, the search exactly as before), reset at Log Out.
Settings' Smart search section after On this day: the switch with a Beta badge
(`SmartSearchLabel`), the footer; turning it off asks first (`off_title`/`off_text`/`off_confirm`), and
a refusal (`smart_search_unavailable`, the demo) shows the device's words. While it is on, the search
panel's first row is "Search photos for “…”" (`SearchOption.smart`), what the Search key takes unless
the words are exactly a tag or a person - that is taken, and the row follows the things and people
(`smartAfter`); a partial match never wins ("beac" searches, "beach" is the tag), the arrows do; the
documents row stays after it; never in Files' own field. Picked,
the words become `PhotoGalleryVM.smartText`, a chip with a sparkle after the tags (both the narrow
field's chips and the wide top bar's; × `clearSmartText`), sent as `SearchPhotos.text` with the chips'
tags, people, collection and area, never `before` or `newer_than`; `ranked` (tags or words) means no
months, no date buckets or scrubber, no jump or upward pages, and with words no person header or On
this day strip. `ListOfFiles.smart_search_reading` shows `app.photos.smart_search.reading` above the
results, announced politely, until a page says false (nothing asked again for it). `smart_search_off` (turned off elsewhere: the switch
goes off too) and `smart_search_unavailable` drop the words and run the search again, saying the
device's words or `smart_failed`; the switch going off drops them as well. The model on demand
(release 152; Android has the same names): `SmartSearchSupport.model` (`SmartSearchModelState`
ready / missing / downloading(percent, problem), from the same answers; absent = an older device =
ready; ready again at Log Out). The row, the Search key and `searchText` need `searchable` (on and
ready). Under Settings' footer `SmartSearchModelLine` shows `download_note` (missing, only while
the switch is off - on, by default since 152, nothing until the device says it is downloading:
`words(on:)`, Android's rule) or a `ProgressView(value:)` and `downloading`, the device's `problem` in
its place (downloading): one
VoiceOver element, the line as label and, with a problem, `ios.storage_percent` as value; the section
announces only the start, a problem and the end (`SmartSearchModelState.announcement`, Android's and
the web's). SettingsView asks `GetSettings` (`refreshSmartSearch`, Smart search's part only) every 3 s
while the switch is on, the model downloading and the app active (`.task(id: smartPolls)`,
`SmartSearchSupport.polls`), and once after the switch moves while the model isn't ready; turned on,
it is downloading(0) at once (`modelRequested`). A search told `smart_search_downloading` drops the
words as `smart_search_unavailable` does, keeps the switch on, sets downloading and asks where the
model is. Outside Settings, Images' search field taking the focus while it is on and the model isn't
ready asks `GetSettings` once, at most every 5 s (`searchFocused`), so the row comes without a visit
to Settings; MainView's top bar follows `searchableChanges` (no redraw at each step of a download).
`deviceSaid` sets the model before the switch, so the row never shows for a moment. Nothing assumes
the user turned it on: off by default in release 151, on by default since 152, the app shows what
the device says.
**Background work on iOS** (release 152; `BackgroundWork.swift`, `BackgroundWorkTests.swift`;
Android's `BackgroundWork`, the same rules; texts `app.settings.background.*`): Settings' In progress
section (`InProgressSection`, heading `app.settings.progress.title`), right after Status and Storage,
holds this phone's uploads first (`UploadDetail`, while `BackgroundWork.showsUploads`) and then lists
`Status.background_tasks` from the Status answer Settings already asks every 5 s while it shows
(`StatusViewModel`), with the background footer only while that list isn't empty: a row per task in the
device's order (a kind once, the first; no kind = `#<index>`), the device's `text`, "done of total"
(`app.settings.background.count`, numbers as the app's other counts) over a `ProgressView(value:)`,
or a spinner and no count while `total` is 0; done kept within 0...total. One VoiceOver element a
row: the words, the count as value.

**Storage, Shut down device and Image Tagging on 4 GB on the phones** (2026-11-03, issues #208,
#202, #209; both apps, the same names; texts `app.settings.storage.*` - copied from
`web.settings.storage.*` with their translations and reviews -, `app.settings.update.shut_down*`,
`app.settings.tagging.heavy`). Settings › Storage, right after Status (iOS `StorageSection.swift`:
`StorageSection`, `MirrorDiskViewModel`, `MirrorDisks`; Android `ui/settings/StorageSection.kt`;
tests `StorageSectionTests.swift` <-> `StorageSectionTest.kt`), ports the web's MirrorDiskPanel: shown
while `Status.mirror_candidates` isn't empty or a disk asked for from here is on its way; a disk is
"<model> · <size>" or its size alone (`MirrorDisks.sizeText`: decimal units, at most one decimal in
the app's number format, 999.96 GB is "1 TB"); two offered disks of one model and size get the
look-alike hint instead of the red "Use it for the mirror"; the confirmation defaults to Cancel. After
`UseMirrorDisk`'s Ack the model follows every Status answer (Settings' poll asks every 2 s meanwhile -
iOS `StatusViewModel.setFast`, Android `start(FAST_POLL_MS)` - and counts each answer, equal ones too:
iOS `onReceive(status.$status)`, Android `StatusViewModel.State.asks`): "Erasing … and adding it to
the mirror…" with a spinner, then the copy's text, bar and RAID state while `RaidSyncing`, gone once
`RaidInSync` or a copy that stopped, and "wasn't added. Alerts says why." when nothing copies 20 s
after the Ack (`MirrorDisks.step`); a refusal shows the device's words, else `storage.failed`. Shut
Down Device sits under Restart Device in Device version (iOS `UpdateSection`/`UpdateViewModel.shutDown`,
Android `UpdateSection`/`UpdateViewModel.shutDown`), the same style, owner of the primary only, both
buttons disabled while either runs: a confirmation (Cancel default, red Shut Down), `ShutDownDevice`,
and its answer read by `ShutDownAnswer` - the Ack shows "Shutting down… Once its lights stop blinking
you can unplug it." (a power icon, no spinner: nothing is waited for), `unknown_payload` DeviceNeedsUpdate
(`shut_down_needs_update`, Update offered), other refusals the device's words, else
`shut_down_failed`. The Ack starts `DeviceShutdown` (iOS `DeviceShutdown.swift`, Android
`net/DeviceShutdown.kt`): for 90 s `OTCConnection.ensureConnected` throws `DeviceShutdown.Quiet` (the
note's words) instead of dialling, its disconnect retry waits the moment out, and Settings' Status
keeps its last answer, so no "isn't available"/"can't connect" card covers the note and no poller
dials a device powering off; `invalidate()` (new credentials, Log Out) forgets it, and the section
drops the note once the device answers again after it. Image Tagging (`MemoryTiers.taggingHeavy` /
`State.withSettings`' `imageTaggingHeavy`): `Settings.image_tagging_heavy` true (under 6 GB) and the
switch available puts `app.settings.tagging.heavy` under the switch ("On this device Image Tagging
uses about 1–2 GB more memory, and new photos take longer to process."), whether on or off; the
footer now ends "It needs a device with at least 4 GB of memory."

**Resumable uploads and big syncs** (issue #205, device release 166; iOS `ChunkedUpload.swift` +
`LongSync.swift` <-> Android `net/ChunkedUpload.kt` + `sync/LongSync.kt`; tests
`ChunkedUploadTests.swift` + `LongSyncTests.swift` <-> `ChunkedUploadTest.kt` + `LongSyncTest.kt`,
the same cases against a stand-in device). Every `BeginUpload` carries `sha256` (the hash the
hash-first step made; required by `uploadChunked` / `ChunkedUpload.upload`), and the chunks start at
`UploadStarted.received`: the part before it is skipped (a seek, a `Data` slice, `InputStream.skip` -
`skipFully` reads a byte at a time only from a stream that skips nothing), never read out to be sent.
An `UploadChunk` that fails on the way (the connection gone, `SentNoAnswer`, the device answering an
error other than `storage_full`, or a `received` it didn't expect) is begun again with the same key
after 2, 4, 8, 16 s (`resumePause`) and goes on from what the device holds; a `BeginUpload` that
doesn't get through then is another try. The photo sync's uploads don't go on over mobile data with
"Wi-Fi only" on (`mayResume`, asked after each pause: the phone may have just left its Wi-Fi). At most `maxResumes` / `MAX_RESUMES` (4) in a row without
the device getting further than it ever had (`furthest`: an older device, which answers 0, has to
pass that point again before a cut counts as new); then the error is the caller's, as before (the
retry list, whose next run resumes it while the device keeps the upload: 30 idle minutes). The first
`BeginUpload`'s failures, `storage_full` and `FinishUpload` (the device may have made it the file;
its hash check stays) are never resumed. `progress` / `onSent` reports the bytes the device holds.
Big syncs share `BackupProgress` (by asset, plus the share of those on their way from the uploads'
bytes, 100 units an asset), the 300 MiB threshold and the texts (`app.shell.backup_title` "Backing up
N photos and videos", `app.shell.backup_progress` "N of M"). Android: a run whose list (new, retried,
one-off; MediaStore `SIZE`) holds more than `LongSync.THRESHOLD_BYTES` with the app in front
(`PhotoSync.inFront`: Android 14 starts a data-sync job only then) enqueues
`SyncScheduler.LongSyncWorker` (unique, KEEP). It goes into the foreground at once (`setForeground`,
`FOREGROUND_SERVICE_TYPE_DATA_SYNC`; WorkManager's `SystemForegroundService` is declared `dataSync`
in the manifest, with `FOREGROUND_SERVICE` and `FOREGROUND_SERVICE_DATA_SYNC`) with an ongoing
notification (channel `backup`, `android.backup_channel`, quiet: the title, "N of M", a bar, Cancel),
waits for the run in flight (the app's own, or a worker WorkManager may stop after 10 minutes), then
runs once more itself, which nothing stops (WorkManager ignores the scheduler's stop for a foreground
worker), picking up what was left. Android refusing the foreground (the app left meanwhile) leaves
everything as before. Cancel (`LongSyncCancel`) stops the run (`PhotoSync.stopRun()`: Log Out's
cancel without the generation) and the job, which isn't offered again until the app comes to the
front again (`LongSync.declined`). iOS 26: Sync All (also the sync question's "all": `syncAll()` sets
`ownerAsked`, taken by the next pass), with the app active, something to send and more than 300 MiB
of it (sizes from `PHAssetResource.dataSize`, iOS 27; before that any), asks for a
`BGContinuedProcessingTask` (`LongSyncTask`: identifier `cloud.off-the.OffTheCloud.backup.sync`
under Info.plist's `cloud.off-the.OffTheCloud.backup.*`, registered when first asked for, strategy
`.fail`). The system shows its progress (the title and "N of M" as assets are done, `progress` set
every second); the run's end completes it (`LongSync.runEnded`, from `runPasses`), and its
expiration (the owner stopping it, or the system) stops the run (`stopRun`). While it holds the run
neither the app's own background time running out nor the end of a BG processing task that joined
the run stops it (`SyncScheduler.stopsRunAtExpiry`). Below iOS 26, and for any other run, as
before.

**Copies of what the iPhone sends** (issue #210; `ExportRoom.swift`, `PhotoSync.exportAsset`; tests
`ExportRoomTests.swift`, `PhotoSourcesTests.swift`, and `PhotoKitLargeTests.swift` against the
simulator's own Photos). Until then the sync copied each asset out of Photos into a temporary file,
mapped it into one `Data` to hash and send it, and gave up for good on anything over 1 GB
(`AssetTooLargeForMemory`, recorded as done): a few minutes of 4K or ProRes video never reached the
device, and nothing said so; the post composer and Free Up Space had the same ceiling. Now the sync,
the composer and Free Up Space all take `exportAsset` - PhotoKit's stream into a temporary file,
cancellable, the copy removed when its asset is done - hash it with `OTCConnection.sha256Hex(of:)` (a
chunk at a time, cancellable) and upload `.file(url)`: no size is too big, and memory stays flat (a
2 GB 4K HEVC video copied, hashed and sent with a cut mid-way: 5 MB more at the peak). The chunked
upload's file reads are drained one chunk at a time (`autoreleasepool`): until then every chunk read
stayed in memory until the upload ended - 2 GB for that video - and the Mac app's upload and its setup
image check read the same way (fixed with it). What bounds a copy is room on
the phone (`ExportRoom`): it always leaves `floorBytes` (256 MiB) free
(`volumeAvailableCapacityForImportantUsage`). A copy whose size is known - `PHAssetResource.dataSize`,
iOS 27, or a lower bound learnt from a copy that stopped for room (`learn`, until the app quits) - holds
that much while it runs, waits while the run's other copies hold room it needs, and fails only when it
doesn't fit alone. A copy under way looks every 64 MiB (`checkEvery`) and stops below the floor, as
does a write the disk refuses (ENOSPC), cancelling PhotoKit's read; both are `ExportNoRoom`. The
asset then goes on the retry list (`Outcome.noRoom`, never `skip`, which is now only an asset with no
resource), and a pass that went through with one says so in Settings › Uploads: `SyncHalt.phoneFull`,
`ios.uploads_phone_full` and `ios.uploads_phone_full_detail` (the file's name and the room it needs;
the composer shows the title). Android sends from MediaStore's stream with no copy, so it has no
counterpart. The assets earlier versions gave up on are found by the one-off pass `large`
(`PhotoSources.large`, queued once: `State.largeQueued`), which follows the floor like the catch-up
(none before a Sync From Now, none with no floor known) and takes only videos with no hash recorded
whose size is unknown or over the old 1 GiB ceiling (`mayHaveBeenTooLarge`); its run always lists the
device folder (`listForLarge`), so videos synced before are matched by path or cloud id, never read.

**Phone app invariants** (both apps unless named). Photo sync's watermark is written per chunk,
never set to "now" at the end and never past the run's start (iOS `lastSyncDate`, Android
`DATE_ADDED`); one ahead of the clock is pulled back, and a chunk boundary inside one date is backed
off so that date is fetched again. Failed assets go on a retry list, not the watermark (iOS
`asset_sync_pending.log`, Android `asset_sync_pending.txt`); on iOS one PhotoKit can't read with Sync
from iCloud off waits on its iCloud side until that is on, but never one whose copy was stopped (a run
cancelled mid-copy) or whose own file failed (`PhotoSync.waitsForICloud`); a run sends new, then retried, then
one-off assets, and goes once more for a trigger that came meanwhile (`rerunWanted`). Its writes are
gated by generations bumped by `cancel()` (Log Out, which cancels again right before the wipe) and
by Sync All and Sync From Now. Settings' Sync From Now asks first (`app.settings.sync.from_now_title`,
`_text`): it gives up on everything not sent yet, the retry list included, and sits next to Sync All. "Wi-Fi only" holds a run before it and between chunks ("Waiting for
Wi-Fi", Android UNMETERED jobs), `storage_full` stops it ("Your device is full", 30 min), a small run
(< 100 assets) skips the folder's listing, and between chunks the transfer hold lets a put-off route
check run. What the viewer's Save wrote stays out of the sync and Free Up Space (iOS `SavedToPhone`,
Android `Pictures|Movies/Off The Cloud/`). iOS BG tasks join the run in flight (`runInBackground`).
A photo whose name the device holds with other content goes to
`<stem>_<first 8 hex of SHA-256(localIdentifier | MediaStore id)>.<ext>`. After the device rejects
Auth (an empty `Ack.code`, or `too_many_attempts`) `OTCConnection` fails the same credentials
without dialling for 5 s doubling to 300 s; success, `invalidate()` or new credentials lift it;
both show the can't-connect card, never `statusCode`. A request is sent again only if it never left
(`retriesAfter`; else `WSClient.SentNoAnswer`), and ws:// goes only to the home network
(`CleartextRule`, before dialling and for media URLs) - and only over the pinned home link once
this phone has reached its device there (Home network, above).
Files open, versions, trimming and the gallery's video fallback read 4 MiB `ReadFile` pieces
(`FileDownload.swift` / `ChunkedDownload.kt`); `GetFile` stays for HEIC and as the
`unknown_payload` fallback. iOS keeps its temporary copies in folders of their own
(`TempFiles.swift`: `tmp/export|viewer|preview/<launch>/`): the photo sync's, the composer's and
Free Up Space's copies of an asset, the viewer's whole-file video (removed with its player) and
Share's picture (once the sheet closes), Files' Quick Look copies (as the preview closes); each
launch removes earlier launches' folders and the copies older versions left at tmp's top level
- an app suspended mid-run and then ended used to leave multi-GB videos there. Android's are in
`cacheDir/tmp/<kind>/` (`ui/common/TempFiles.kt`, `TempFilesTest.kt`): the viewer's whole-file
video (no stream URL; deleted as the viewer moves on or closes), Files' opened files and versions
(under their own name, one at a time) and Share's picture and logs (one at a time), the composer's
trims and the feed's fallback videos; each process start moves the folder aside and deletes it,
with the plain files older versions left at the cache's top level (all but
`social_feed_cache.pb`), and the FileProvider grants only `cacheDir/tmp/` (`file_paths.xml`). Android keeps at most 3 `UploadChunk`s in flight (OkHttp closes a
socket past 16 MiB queued). Android only: backup and device transfer include just
`otc_settings.xml` (`data_extraction_rules.xml`, `backup_rules.xml`) - new prefs and files stay out
unless added there on purpose; `SecretsStore` recovers an `otc_secrets` restored without its
Keystore key by resetting to a fresh install - only when its keyset can never be read
(`keysetUnusable`: a bad tag, a key that isn't there, a corrupt file); any other Keystore error is
tried again (0.2, 0.5, 1 s), then fails that use (`SecretsStore.Unavailable`) with the file kept:
RootView says `android.secrets_unavailable` with Try again and Start over (asked first,
`SecretsStore.startOver`), a background run is retried, and a read that failed once never
replaces the device id. Such an error can be for good too (a key whose blob the Keystore no
longer takes after an OS or firmware update): how long it has lasted is kept in the plain,
not-backed-up `otc_secrets_health` prefs (`SecretsFailures`: the first failure and the launches
since, forgotten at the next open that works), and once it has failed on 3 launches over a day at
least, the store starts over as for a restored file, the master key made again if need be (tests:
`SecretsRecoveryTest`); `PhotoSync.hasPermission()` must match
`mediaPermissions()`. Both: connection forms edit drafts and write `SecretsStore` only on Save
(`saveCredentials`; options `persistOptions`), since every reconnect dials the stored values. iOS
unit tests: `OffTheCloudTests` (Swift Testing), on a simulator with `-only-testing:OffTheCloudTests`.

**The profile in Settings** (iOS `ProfileEditor.swift` <-> Android `ProfileEditorViewModel` in
`ui/settings/SettingsViewModels.kt`; tests `ProfileEditorTests.swift` <-> `ProfileEditorTest.kt`, cases
P1-P5): the crop's "Use photo" saves at once (`usePhoto`: SetProfile with the name and text as they are,
only once the profile has been read - else the photo waits for Save), and a read again (iOS runs the
section's `.task` each time it comes back on screen after a scroll - not when the crop's full-screen
cover closes, checked on the simulator; Android each time Settings is shown) changes only the fields
still showing what was last read or saved (`ProfileFields.merge`), a read asked before a save that
worked being dropped. Until 2026-11-05 a photo picked waited for Save, and leaving Settings and coming
back read the old one again. Android saves in the view model's scope, so leaving Settings doesn't stop
it.

**Drag to select** (issue #200, iOS `DragSelect.swift` <-> Android `ui/common/DragSelect.kt`):
Images, People and Files (list and grid) select by long press, and a drag on without lifting
adds every item from the pressed one to the one under the finger, in display order (shrinking
back restores what the range passed over; a press never deselects); while anything is selected
a tap toggles. On iOS it is a UIKit `UILongPressGestureRecognizer` through
`UIGestureRecognizerRepresentable` on the lazy stack/grid/List (SwiftUI's own long press inside
a ScrollView or List holds the scroll up), refusing to begin unless the press is inside an
item's frame (`gestureRecognizerShouldBegin` and `began` use `DragSelectFrames.item(containing:)`:
a gap, the empty slots after a short row, padding, a title, Files' frameless `..`, or a face's
name or count or its "more" button start nothing - People's `faceToStart` refuses a point inside
a second `DragSelectFrames` of the buttons, recorded inside their offset; moves take the nearest
item in the row, `item(at:)`); items record
their frames with `onGeometryChange` plus
on-screen counts (`DragSelectFrames`), in a name on the lazy stack - but a List's rows don't see
a name given to the List, so Files' list uses `.global`. The edges scroll the found
`UIScrollView`'s `contentOffset` from a `CADisplayLink` (bands of min(56 pt, a third of the
visible height), none under 8 pt, up to 1200 pt/s, only once the finger has moved more than
10 pt from where the press fired, `DragSelect.armed`). A pressed item gone mid-drag (a refresh)
ends the drag (`DragSelection.goesOn`), and so does a new Files listing (`load()` drops the drag
where it sets the selection, none): the selection stays as it stands, `moved` answers false and
the gesture stops following and scrolling, and the lift is still no tap; the item under the
finger gone keeps the selection until the finger is over another (`range(in:)` is nil). A pick
made before the press that goes mid-drag (People's listing `onChange`, Images' deletes through
`items`) leaves the drag's base too (`DragSelection.forget`), so the next move doesn't bring it
back. Files' long-press menu is gone: Share as gallery and Keep out of / Show
in Images are in the bar when one folder is selected (as Android's), upload only stays on the
list's lock, and a grid tile's lock asks to allow deletions. Tests: `DragSelectTests.swift`; on
a simulator, idb can hold then drag through its Python client's HID events (touch down, delay,
moves, up) - `idb ui swipe` can't. On Android it is `Modifier.dragSelect` on the LazyColumn/grid
(a pointer input on the Initial pass, so after the long press the moves and the lift are the
drag's), hit-testing the lazy layout (`keyAt`; Images' `GalleryRow.itemAt`); a drag starts only
where `rememberDragSelect`'s `start` finds an item (People: `onFace`, the face but not its name,
count or "more" button), and the edges scroll (bands of min(56 dp, a third of the list), none
under 8 dp, `dragScrollSpeed`) only once the finger has moved more than the touch slop from where
the press fired (`DragSelectState.armed`). The view models' `dragGoesOn` is asked at every move
and before every scroll step (`DragSelectState.goingOn`), not only when another item comes under
the finger: once the pressed item is gone (`DragSelection.goesOn`, for good even if it comes
back) or a new Files listing has landed (`load()` drops the drag before it sets the selection,
none), the drag is over with the selection as it stands - moves change nothing, the edges stop,
and the lift is still no tap; the item under the finger gone keeps the selection until the
finger is over another (`range` is null). A pick that goes mid-drag (People's `keepOnly`,
Images' deletes) leaves the drag's base too (`DragSelection.forget`), so the range doesn't
bring it back. Tests: `DragSelectTest.kt`, `PeopleViewTest.kt`, `PhotoGalleryRetryTest.kt`.

**VoiceOver where only a gesture reached** (iOS): Images' date scrubber is an adjustable element
(`app.photos.grid.scrubber`, the web's slider: its value the month, `PhotoMonths.spoken`; a swipe up
or down jumps a month newer or older at once, `PhotoGalleryVM.scrubStep`); the viewer's photo says
what its tile says, a three-finger swipe or the Next/Previous photo actions page through it and say
where (`ViewerPaging`), the neighbours waiting off screen hidden; the trimmer's handles step a
hundredth of the clip a swipe (`DualRangeSlider.step`); a Files grid tile is one element named by
its file with Select, versions, the lock and kept out of Images as its actions (`GridTileAccess`); a
comment's like count says "N likes"; Files' path field is named (`app.files.path_label`). Tests:
`VoiceOverTests.swift`. Android does the same for TalkBack: the scrubber's grab strip is a slider
(`progressBarRangeInfo` oldest to newest, one month a step, `setProgress` ->
`PhotoGalleryViewModel.scrubStep`; `scrubberMonth`, `scrubStepBucket`, `monthSpoken` in
`PhotoMonths.kt`; `State.jumpMonth`); the viewer's page is named as its tile, has Next/Previous photo
actions, announces the page a swipe settled on, and the neighbours are hidden; the trimmer's
handles are two named elements over the slider stepped by `trimStep`
(`android.trim_start_handle`/`_end_handle`); a Files grid tile is one element with its badges as
actions and the versions badge reads `android.files.older_versions`; Settings' section titles are
headings. Tests: `PhotoMonthsTest.theScrubbersMonthAndSteps`, `ComposerTest`.
**Text size in the wide layout on iOS** (`AppMenu.swift`): the menu, the rail, the top bar's field
and chips and the Collections page keep their design sizes (11-16 pt) at the default text size and
follow the reader's text size from there (`scaledFont`, a `@ScaledMetric` relative to a text style),
names cut short shrinking first; the rail, a column of fixed width, grows up to xxxLarge and shows an
item large while pressed (`accessibilityShowsLargeContentViewer`), as a tab bar does.
**Leaving selection mode on iOS** (`SelectionActionBar.swift`, Images and Files): the count pill
reads [× N selected]; the × (`onClear`, named `app.shell.clear_selection`) runs the view model's
`clearSelection()` - everything unpicked, a drag under way ended (its next moves pick nothing),
Images' `pickedAway` dropped, a tap opens again; Files keeps the bar for Upload. The × takes only
its glyph's room but lays out and answers touches over a 44 pt square (`TouchTarget`); it is
drawn over the pill (`overlay`, a hidden glyph holding its place), not inside it, because the
iOS 26 glass takes no touch past its own edge for what it holds, and the pill sits 10 pt above
the bar so the bar's items don't take the square's bottom. `.keyboardShortcut(.cancelAction)`
gives an iPad keyboard's Escape the same (a dialog or sheet over the screen takes Escape first).
People keeps its own toolbar ×. Tests: `DragSelectTests.swift` (`SelectionBarClearTests`).

**Leaving selection mode on Android** (`ui/common/SelectionActionBar.kt`): the "N selected" pill
above the bar starts with an × (`onClear`, named `app.shell.clear_selection`) that runs the
screen's `clearSelection` - everything unpicked, a drag to select ended, a tap opens again; Files
keeps the bar for Upload. The pill stays compact (`SelectedPill` draws its Surface under the row,
so nothing clips the ×) while the × lays out at 20 dp but is measured and touched as a 48 dp
square (`touchTarget`), the pill above the bar (`zIndex`) so those touches reach into the bar's
top padding. Back clears too: Images' `BackHandler`, Files' `filesBack` - the search's results
first (they hide the folder and its bar), never while a search is open; the viewer, dialogs and
menus take Back first in their own windows. People keeps its own ×.

**Copies on Android** (issue #201, `PhotoGalleryViewModel.kt`): `Item.copies` is `File.copies`
(null when unset); `copiesAsk` picks the dialog (`CopiesDialog`: one photo with copies above 1,
`app.photos.copies.single_text`, "Delete only this copy" / "Delete all N copies"; a selection with
any, `several_text`, "only these" / "every copy"; else the old confirmation). `delFileFor` sends
the tile's `hash` on every delete and `every_copy` only where copies is above 1; Files' rows send
their hash (`filesDelete`, files only). A refusal keeps the tile and shows the device's words in
`app.photos.notice.delete_failed`; `changed` closes the viewer and starts the search again (the
Files viewer lists its folder again; Files' own delete toasts the words). Picks a jump or a
reload takes off the grid keep the `Item` last shown (`pickedAway`: `keptPicks` before
`performJump` / `resetAndLoadFirstPage` empty the grid; `pickedByPath`, the grid's own first):
`deleteSelected` sends their hash and every_copy and the copies question counts them
(`pickedItems()`), as iOS's `pickedAway`; unpicking, clearing or deleting forgets them. `removeDeleted` lowers
the `have` of the search that brought the photo. The viewer's Save downloads the original with
`ChunkedDownload` and inserts the bytes as they are through MediaStore (`IS_PENDING`, `DATE_TAKEN`
= the photo's date, its name and MIME, `saveFolder`: `Pictures/Off The Cloud`, a video
`Movies/Off The Cloud`), in the view model's scope; Save spins meanwhile (`State.saving`); only a
type the library refuses is saved as the shown picture in JPEG, as before.

**Photo edit on Android** (2026-11; `ui/gallery/PhotoEdit.kt` holds the pure rules and crop maths,
`PhotoEditor.kt` the full-screen editor, `PhotoEditTest.kt` and `PhotoGalleryEditTest.kt` the tests,
and the debug-only `PhotoEditPreviewActivity` shows it on samples with a pretend device). The
device side is in the Photo edits bullet under Architecture.
- **Edit and Revert to Original** sit next to the viewer's Info (the top bar in portrait, the
  leading column in landscape) wherever the shared viewer opens: Images, collections, Favourites,
  Files and its search, Memories, the map.
- **When they are offered:** Edit for `PhotoEdit.editable` (JPEG, PNG, HEIC/HEIF by type or a
  `.heic`/`.heif` name, AVIF, WebP; never a video, GIF, raw, TIFF or SVG). Revert when
  `Item.originalHash` (`File.original_hash`) is set.
- **The editor:**
  - It shows the viewer's own picture (`viewerImage`) with a crop frame: handles, the outside
    dimmed, thirds while dragging.
  - Rotate Left/Right turn the crop with the picture; Aspect fits the largest frame of that shape
    centred on the current one; Reset; Save only when something changed.
  - The edit in progress survives the activity being recreated.
- **Save:** save_title asks Replace or Save as Copy. Replace asks GetEditState first (60 s; failing
  or older counts as 1). With copies > 1 it asks copies_text, and "All" sends `every_copy`.
  EditImage then sends the shown `hash`, and Saving… blocks the editor.
- **What it shows after:** Replace puts the returned File in the item's place, with a picture
  turned and cut on the phone (at most 1024 px) until the full size comes. The in-flight full-size
  fetch is keyed by item, so the old content's late answer can't land on the edit. Save as Copy
  inserts the copy next to the photo, in the whole-library view only, and says saved_copy.
- **Refusals:** "changed" from either request closes the editor and viewer and reads the photos
  again, as Delete's does. Other refusals show the device's words in the editor (revert's in the
  viewer's alert). The demo's refusal and an older device's `unknown_payload` show the device's
  message.
- **Behind the viewer:** Files, Memories and Places re-read their own listing.
- **Videos** (2026-11-08; `object VideoEdit` in `PhotoEdit.kt`, the screen `VideoEditor.kt`;
  tests in `PhotoEditTest.kt`, `PhotoGalleryEditTest.kt`; the debug preview has three pretend
  videos, `--ei photo 3`, and `debug/assets/video-cut-sample.mp4`): Edit on a video the device can
  cut opens the composer's trimmer (`VideoTrimmerView` with options: Save, the cut note, no Remove
  trim, `TrimHandles` kept through a rotation; the player through `MediaStream.player`, so the
  pinned home stream plays) on the viewer's `videoUrl`, the viewer's player paused. Save asks as a
  photo's (`video_save_text`, `video_replace`, `video_copies_text`); EditVideo waits as long as a
  post (30 minutes); an end within 0.05 s of the video's goes as 0. The viewer's video fetch is
  keyed by item id, not path - a cut keeps its path, and the old content's late stream answer
  played in its place. A video's Revert says `video_revert_text`.

**Also delete from this phone on Android** (owner's request 2026-11-03; `ui/gallery/DeletePhoneCopy.kt`:
the rules `DeletePhoneCopy`, `DeletePhoneCopyModel`, `DeletePhoneCopyDialog` in RootView; tests
`DeletePhoneCopyTest.kt`, cases D1-D9 as iOS's, and `DeletePhoneCopyGalleryTest`; texts
`app.photos.delete_phone.*` and `android.photos.delete_phone_text`; iOS must match): once the device has
accepted a delete - the viewer's (`deleteCurrentPhoto`: Images, People, a collection, Places, a memory's
See all, Files' viewer), the selection bar's (`deleteSelected`) and Files' own (its photos and videos by
MIME, `isMedia`) - the view model hands what went to `deletedOnDevice` (a `DeletedOnDevice` each: the
hash shown, `File.copies`, every_copy; tests listen there), and the model asks, one delete at a time,
whether to move the same photos to the phone's trash. Only content the device no longer holds counts
(`gone`: every_copy, or copies unset or at most 1 - never "Delete only this copy"), matched by hash
with what this phone recorded for its own files - AssetSyncCache (what the sync sent) and Free Up
Space's `free_up_hashes.json` (whichever version; both `idsWithHashes`, `recordedUnder`) - for rows
still in MediaStore (not in its trash) and never in `PhotoFolders.savedFolders`; then
`FreeUpSpace.held` (HasHashes, HasFile on an older device) and what the device still holds is left
out (Files' rows count no copies); no answer, no question. Only on Android 11+ with full media access
(`asks`: not Android 14's picked photos, as iOS's limited access), with the app in front and the
endpoint the delete went to. The dialog lies over whatever is on screen and outlives the viewer:
`title` (one of the deleted on the phone) or `title_several`, then `android.photos.delete_phone_text`
around `photo`/`video` (a single delete) or `some` (how many of several); Keep on Phone is the dismiss
button, Delete from Phone red. Delete from Phone reads the rows again and has RootView launch
`MediaStore.createTrashRequest` (500 a request, as Free Up Space; Android confirms, the trash keeps them
30 days); moved, their ids leave the sync's retry list (`AssetSyncCache.pending.resolve`, as for a file
the sync no longer finds), and nothing is sent back - they are no longer in the library; declined,
nothing more; a request that can't be made or launched says `app.photos.delete_phone.failed`. Kept on
the phone, a photo goes back to the device only with Sync All or a one-off pass, as any photo it no
longer holds. Free Up Space itself is unchanged (`held` is `internal` now).

**Largest files and Compress on Android** (2026-11-08; `ui/files/LargestFiles.kt` +
`LargestFilesView.kt`, `ui/files/Compress.kt` (`CompressRules`, `CompressModel`) +
`CompressDialog.kt`; tests `CompressTest.kt`, the web's case ids; debug-only
`CompressPreviewActivity`, `--es show largest|compress|bar`, `--es device demo|old|slow`).
Settings › Largest files (`LargestFilesSection`, after In progress) opens a full-screen list: pages
of 50 as its end nears, a failed page asked again 1 s doubling to 10 s, `unknown_payload` ->
`largest.needs_update`; a row per content (small thumbnail by path, else `FileTypeIcon`; name,
folder, size, "in N places"); photos and videos in a viewer of their own (copies set), other files as
Files opens them (`fetchToPreview`, shared with Files); what the viewer deleted leaves at once, and
the list is read again from the top as far as it was shown (`reread`, at most 1000 rows) after an
edit, a delete or a job that compressed something. `SelectionActionBar`'s Share, Download and Delete
are optional: the list's bar has Compress alone, Images' has it before Delete. The dialog's model is
one per screen and outlives a rotation: the web's rules (first 500 photos and videos, the radio and
switches, the sums, the reasons); Compress sends CompressFiles (no answer: `GetCompressJob("")` is
asked before saying it failed), clears the selection and follows the job every 2 s while the dialog
shows it and every 10 s while Images or the list is in front, never in the background; each done
result swaps its tile (`compressed` -> `swapItem`) or row. `Item.originalRemoved`: no Revert
(`PhotoEdit.revertable`), Info says `compress.compressed_note`.

**Newer photos above a jump on Android** (issue #194): a jump sets `State.upOn` and a second
cursor (`upToken`, `upHave`, `upEnd`; `askUp`/`fetchUpPage`, `upFailed`'s backoff, `retryUp`, a
Wake); the downward `have` is `downHave`, never `items.size`. The room above (`roomAbove`,
`layoutGallery(above =)`): a first month from the upward search keeps its bucket's count with
grey slots first (`lead` on `TileRow`/`Segment`), a complete one gets the next newer bucket's
month in grey above it. Rows are keyed by month and place (`GalleryLayout.keyOf`, "2024-03#0"), so
photos land in the grey tiles with no key changing and the LazyColumn keeps what is on screen;
`keepRowsInPlace` (the frame before, `DrawnRows`) holds the first row still there when one went
(a miscounted bucket, the room dropped). Nothing is asked at the jump - its landing puts its
month's title at the top in the SideEffect (`requestScrollToItem`), so the room is never drawn -
only once a grey tile is (`rowShown(key, start, end, lead)`), and after that for the first
`UP_NEAR` (12) photos too; each page redraws the rows' effects (keyed on `upLanded`,
`composedRows` cleared). `oldest_first` false drops the page and the room, not asked again until
the next sign-in (`OTCConnection.signIns`). A failed page is the foot's words and Try again in a
pill at the top while near it. The viewer's pager is keyed by item id.

**Storage view on Android** (issue #195, `ui/files/FilesUsage.kt`): `FilesView` (List, Grid,
Storage) in View options, kept in `files_view_mode` ("storage"; older builds read it as the
list). `askUsage` runs on `setView` and every `load()` while Storage shows, never from List or
Grid; answers for another folder are dropped (`usageFits`, a sequence); `unknown_payload` shows
`app.files.usage_needs_update` until the next sign-in. Rows keep the device's order, joined with
the listing by (path, is_dir) for the icon, lock and eye; size is files + versions, the share a
whole percentage, rounded (`usageShareText`: "under 1%", no bar at a 0 total); the total
and versions lines above, `..`, the note below; no selection, no Sort by; a folder opens in
Storage, a file as in the list. Tests: `PhotoGalleryUpTest.kt`, `PhotoMonthsTest.kt`,
`FilesUsageTest.kt`.

**Copies on iOS** (issue #201, `DeleteCopies.swift`, `DeleteCopiesTests.swift`): `Item.copies`
is `File.copies` (nil when unset); `CopiesAsk.of` picks the question - one photo above 1 copy
(the viewer's Delete, or one picked): `app.photos.copies.single_text`, "Delete only this copy" /
"Delete all N copies"; several with any: `several_text`, "only these" / "every copy"; else the
old one - taken when Delete is tapped (`confirmCopies`, `deleteAsk`). `DeleteCopies.request`
sends the tile's `hash` on every delete (Files' rows too, files only) and `every_copy` only
where copies is above 1. A refusal keeps the tile and shows the device's words in
`app.photos.notice.delete_failed`; `changed` closes the viewer and runs the search again from
where it began (`reloadSearch`: a jump at its month), the Files viewer lists its folder again
(`onDeleted`), Files' own delete toasts the words (in `delete_failed`, as Images says them); Files now shows its viewer's alerts too
(`ViewerNotice`). Save downloads the original (`FileDownload.download(original:)`, ReadFile
even for HEIC) and adds it as it is (`PHAssetCreationRequest` `.photo`/`.video` resource,
`creationDate` = the item's date), add-only access as before; Save spins (`savingPath`,
`app.photos.viewer.saving`); only a photo Photos refuses is saved as the picture shown.

**Save to this phone** (owner, 2026-11-13; `SaveToPhone.swift`/`SaveToPhone.kt`, tested by
`SaveToPhoneTests`/`SaveToPhoneTest`): the viewer's Save says nothing once done; its button
becomes a white check (`checkmark.circle`, `CheckCircle`) that saves no second copy, remembered by
the view model (`SaveToPhone.key`: the hash, else the path; an edit can be saved again). The
grid's Download asks Save to Photos (iOS) / Save to this phone (Android) or Download as ZIP (the
old share link; Files keeps only the ZIP). The save takes the picks in grid order, and
`SaveToPhone.plan` saves a repeated hash once and skips what this phone's library holds (the
sync's and Free Up Space's records still there, `DeletePhoneCopyModel.phoneAssets`/`inLibrary`,
only with read access) or was saved from here. One at a time through the viewer's `saveOriginal`,
with no fallback picture (a refused type fails, named by `app.photos.download.unsupported`). The
bar holds (`preparing = download`); a pill says "Saving N of M…" with Cancel (`FileDownload` stops
at its next piece) and keeps the screen on; then `app.photos.download.failed`, or a 4 s note.

**Photo edit on iOS** (2026-11; `PhotoEditor.swift` holds the rules, crop maths, requests, flows
and the editor view, tested by `PhotoEditorTests.swift`; the project's folders sync with the disk,
so no pbxproj entry is needed). It works as Android's above.
- **Placement:** Edit (`crop.rotate`) and Revert to Original (`arrow.uturn.backward.circle`, a
  spinner while asking) sit beside Info in `ImageModal`, so they show everywhere the shared viewer
  opens.
- **When they are offered:** `PhotoEditRules.offersEdit` and `offersRevert` (on
  `Item.originalHash`).
- **Save and refusals:** a confirmation dialog asks Replace or Save as Copy, then the copies
  question after GetEditState (asked 0.6 s after the first, so a fast reply doesn't drop it).
  "changed" closes the editor and viewer through Delete's `contentChanged`, as on Android.
- **After Replace:** the item is swapped in place (`editShown`) with a preview drawn on the phone
  (`PhotoEditRender`). The full-size loads are keyed by path and hash (`inFlightHiRes`), and
  `forgetFullSize` drops what was decoded from the old content.
- **After a copy, or "Only Here" of several copies:** the grid is read again as the viewer closes
  (`editReloadOnClose`, as Rating does). Files, Memories and the map re-read through `onDeleted`.
- **Revert** asks revert_title (with revert_copies_text for several copies), and forgets the
  edit's cached thumbnail when no path holds it any more.
- **Videos** (2026-11-08; `VideoEditRules` beside `PhotoEditRules`, `VideoEditorView` and
  `saveCut` in `PhotoEditor.swift`; tests `VideoEditRulesTests`, `VideoEditGalleryTests`): Edit on a
  video the device can cut and AVFoundation plays (`playableHere`: not MKV, WebM or AVI, which the
  cut screen couldn't open) opens the composer's trimmer (`VideoTrimmerView`'s `cut`: `TrimmerCut`,
  Save, the cut note, no Remove trim; its player from `MediaStream.asset(for:)`) on the viewer's
  own source (`videoSource(of:)`), the viewer's player paused. Save asks as a photo's
  (`video_save_text`, `video_replace`, `video_copies_text`); EditVideo waits as long as a post (30
  minutes); `TrimRange.reachesEnd` sends 0 for the end. After a Replace the viewer opens the item
  again on a new stream (`fetchVideo`'s `stillOpen` checks path and hash). A video's Revert says
  `video_revert_text`.

**Also delete from this phone on iOS** (owner's request 2026-11-03; `DeletePhoneCopy.swift`: the rules
`DeletePhoneCopy` and `DeletePhoneCopyModel`; tests `DeletePhoneCopyTests.swift`, cases D1-D9 as
Android's, and `DeletePhoneCopyGalleryTests`; texts `app.photos.delete_phone.*` and
`ios.photos.delete_phone_text`): Android's rules above, case for case. `PhotoGalleryVM.deleteCurrentPhoto`
/ `deleteSelected` and `FilesExplorerViewModel.deleteSelected` hand what the device accepted to
`deletedOnDevice`; the phone's assets come from `AssetSyncCache.identifiers(withHashes:)` (the sync's
hashes and Free Up Space's, which it records there too), those `PHAsset.fetchAssets` still finds, never
`SavedToPhone`'s, then `FreeUpSpace.held`. Only with full Photos access (`.authorized`: limited or none
never asks, and nothing asks for access) and the app active. The question is a `UIAlertController` in a
window of its own at `.alert` level (`show`/`close`: the key window's tint, made key again after), not a
SwiftUI alert: the viewer may close under it (its last photo deleted), and an alert presented from under
the full-screen viewer takes the viewer away. Delete from Phone fetches the assets again and calls
`PHAssetChangeRequest.deleteAssets` (iOS asks; Recently Deleted keeps them 30 days; with iCloud Photos the
deletion reaches iCloud and the other devices, which the text says); done, `AssetSyncCache.resolve`;
`PHPhotosError.userCancelled` says nothing (`declined`), any other error `app.photos.delete_phone.failed`.

**Largest files and Compress on iOS** (2026-11-08; `Compress.swift`: `CompressRules`,
`CompressModel`, `CompressSheet` and Images' glue; `LargestFiles.swift`: the list's rules, model,
screen and Settings section; tests `CompressTests.swift`, the web's case ids). Android's rules: Settings
› Largest files after In progress (heading, "Show Largest Files", the hint as the footer); rows with
their checkbox (or a long press), a thumbnail by path (`GridThumbLoader`, Images' size and cache) or
`FileTypeIcon`, name, folder, "in N places" and size; pages of 50 asked 15 rows before the end, a
failed one with `PageRetry` and Try again; `reread()` in place after a pull, a viewer edit or a job
that compressed something; a photo or video opens in the shared viewer, anything else in Quick Look.
`SelectionActionBar`'s Share/Download/Delete are optional; Images has Compress before Delete, except
among one person's photos (a ninth item cut German labels). The job is asked every 2 s while the
sheet shows and every 10 s while the screen holds the model (`compressFollow`); each done result
replaces its item. `Item.originalRemoved`: no Revert to Original, and Info opens with
`compress.compressed_note`.

**Newer photos above a jump on iOS** (issue #194, `NewerPhotos.swift`, `NewerPhotosTests.swift`):
after a jump `PhotoGalleryVM` keeps a second cursor (`upToken`, `upEnd`, `upLoading`,
`upProblem`, `upRetry`, a wake); each search's `have` counts its own items (`Item.up`), so a
delete lowers the right one. Nothing is asked at the jump: the view arms it when the grid is
scrolled up (`onScrollGeometryChange`, `armNewer` - a pull at the top counts), then rows near
the top on screen (`newerIfNearTop`: first photo under 12, or the top month's grey tiles) ask.
`oldest_first` false drops the page; `DeviceSupport.newerThan` stays off until the next sign-in
(`OTCConnection.connected`). A page goes above newest first, less what is shown by id or hash
(`PhotoPaging.newer`); `openIndex`/`morePendingAt` shift. Rows are named by month and place
(`PhotoGridLayout`, "t2024-03@6"), tiles by photo (`.id`), so the rows on screen keep their
names; `GridAnchor` notes the top row's frame (`.scrollView` space) right before
(`willChangeAbove`) and `proxy.scrollTo(row, anchor:)` puts it back right after
(`aboveChanges`; scrollTo lines anchors up in the whole container height, the tab bar's inset
included), with 0.3 s in which the rows drawn for that moment ask nothing. The top month from
the upward search keeps its bucket's room with grey tiles before its photos (`firstRoom`,
negative indices from `firstSlot`), and a page's months never join the lines under them
(`upBreaks`). Spinner and failure float over the first row (`newerStatus`).

**Storage view on iOS** (issue #195, `FolderUsage.swift`, `FolderUsageTests.swift`): the third
choice of Files' view menu, `files.viewMode` "storage" (older builds read it as the list), no
Sort by there. `FilesExplorerViewModel.load()` asks `loadUsage()` alongside the listing only
while `storageShown` (on opening it, each folder, a refresh, after a delete); answers for
another folder are dropped (`FolderUsageModel.answers`), errors by a sequence;
`unknown_payload` shows `app.files.usage_needs_update` without asking until the next sign-in
(`DeviceSupport.folderUsage`); no answer `usage_failed` + Try again; other errors the device's
words. Rows keep the device's order, joined with the listing by (path, is_dir) for the icon and
the lock / eye marks (by the name; read as the row's value); size is files + versions, the
share a whole percentage in the app's locale or "under 1%", no bar at a 0 total; the total and
versions lines above, `..`, the note below; no selection; a folder opens in Storage, a file as
in the list. No per-folder file count (no catalog key; the web and Android show none).

**Composer, feed, Alerts, Friends and Files after the 2026-10 review** (both apps unless named).
The composer is a new view model per opening (Android `ComposerStore`, a `ViewModelStore` cleared
as the dialog closes; iOS's `@StateObject` per sheet), and Android publishes in an app scope, so
closing it doesn't stop a post. Picks are `ComposerPicks` (ids in post order with their items),
kept across a source or tag change and published from there; a capture switches to Phone and
waits for its page first; `captionNeeded` says "Add a caption to publish". For VoiceOver and
TalkBack each of its tiles is one element (`PickTile`: the photo or video and its date - Images'
tile label, a Synced item's from its File's `created` -, `app.social.compose.position` once picked,
as Android's state description, and selected), its strip's × and arrows are named 44 pt / 48 dp
targets (`app.social.compose.unpick`, `move_left`, `move_right`; the place in the post a badge on
the thumbnail), and a tag chip's × is named as Images' (`app.shell.search_remove`) - a bare "×"
read "multiplication sign". Android keeps Synced
tiles' late thumbnails in `State.thumbKeys`: an `Item` equals another by id, so a copy with a new
key was an equal State the flow dropped. The feed's requests go through `socialRequest` (iOS) and
Android's `FeedSend` helpers (`commentRefusal`, `deleteRefusal`, `fetchPost`): a comment the
device didn't take comes back in its field and the feed's notice (`FeedNotice` / `State.notice`)
gives `refusal()` - the Ack's words, else the envelope's - or `app.social.post.*_failed`; a comment
that worked asks for that post alone (ReqGetPublication). Likes, the comment bin, the post's ...
and mute carry `app.social.post.*` names (the old `android.social_*` keys) and a selected state;
Android's tab bar names Alerts with `app.shell.section_unread`; feed and Alerts times tick each
minute (iOS `TimelineView(.everyMinute)`, Android `rememberMinuteClock`). The GetPublicationMedia
fallback stops above 50 MB (`FEED_FALLBACK_MAX_BYTES` / `fallbackMaxBytes`) and deletes its temp
file with the video; iOS decodes feed thumbnails (`MediaSizeCache.decode`, the box's shape from
the header), avatars and Alerts' pictures (`DecodedImages`) off the main thread, and a profile
photo pick through ImageIO (`ProfilePhotoCropItem.make`). Alerts: `CountPoll` asks the unread
count at once, on a kick (`pushArrived()` from `OTCMessagingService` / `willPresent`, the app back
in front) and else every 45 s; a list that failed says why (`AlertsBody` / `body`, the `alerts`
load subject) with Try again and is asked again while shown; a refused Accept says why. Friends
shows `app.social.friends.your_address` (Settings.domain, else the endpoint's bridge name) with
Copy and Share (`share_text`'s `{address}` is plain text, no isolation marks, so it pastes into a
Friends field); the add field's keyboard Send sends. Its list never reads "No friendships yet." for a
list that didn't come (iOS `FriendshipsViewModel.body`, Android `friendsBody`; Alerts' rule): a spinner until the first
answer, `app.social.friends.load_failed` with Try again when the answer was an error or none came
(asked again at a wake), the list kept while a refresh of it fails. Files uploads go through `UploadQueue` (two
at a time in the order picked - Android's slots take the next file from one list, as iOS's did -, `app.files.uploading` with Cancel, a cancel never reported) in the view model's scope;
iOS reads each through `NSFileCoordinator` and both say `app.files.cannot_read`; Android's
multi-delete runs in the view model's scope. Images' and Files' bulk Delete hold the selection bar,
its Delete a spinner, until every delete is done (`SelectionActionTask.delete` / `DELETE`): through
the bridge a few hundred take minutes, and a second Delete sent the same paths again; Files says
what wasn't deleted as Images does (iOS `DeleteCopies.keptText`, Android `deleteKeptToast` over
`deleteRefusalNotes`: the device's words, or why no answer came - on Android in the app's words,
`deleteFailed` over `OTCConnection.describe`, never the network stack's English) - it said nothing at all. Android only: the "isn't available"/"can't connect"
cards lie over the whole Scaffold (touches consumed, its semantics cleared; iOS hides the tabs
through `tabsCovered`), Welcome scrolls with imePadding, and the window theme is DayNight. Its
colours read at 4.5:1 (`ui/theme/Theme.kt`, `ThemeContrastTest.kt`): light mode's accent is ember
deepened (`EmberDeep`, #B5371C) with white on it - ember itself is 2.7:1 on white -, dark mode's
is ember with ember-ink (#26100A) on it, what removes or signs out takes the theme's `error`
(#B3261E light, #FF8A75 dark; a filled red button `error` with `onError`), a caption that warns
the theme's `warning` (#FF9800 dark, `WarningDeep` #8F4E00 light: the orange is 2:1 on light
surfaces), and the critical update's banner `AlertRed`. No screen hard-codes the old red #E53935
or that orange for text or a button's content (`ThemeContrastTest.noScreenHardCodesTheOldRedOrOrange`
scans the sources; the tour's drawings and the storage bar's fill aren't text). iOS doesn't
follow yet (issue #211: white on ember, ember text in light mode, the system red and orange). The
Bluetooth setup's Back steps back in the wizard (its `#back`) or leaves, asking first while the
device installs (`setupLeaveAsks` / `leaveAsks`); `app.shell.setup_not_found` after 30 s of
looking; Open Settings when access is needed (`ios.open_settings` / `android.open_settings`);
Android also looks again on ACTION_STATE_CHANGED and on return from Settings, with Turn on
Bluetooth, a Location hint below Android 12 and `Phase.ScanFailed` with Try again.

**Known gaps left by the 2026-10 hardening pass.** iOS `FileDownload` falls back to `GetFile` only on `error_code =
"unknown_payload"`, sent since v9 (Android also takes the older bare message) - no device that old
exists. Optional items skipped: device - no `IsContentHash` guard in `mediastream/reader.go`, no
write deadlines in its own HTTP server (`api/`), `cloud_id` kept by
`ReplaceFileKeepingVersion` (`.`/`..` path components and a trailing `/` are refused since 2026-11); bridge - no `interpolateParams`,
`Admin.Logout` answers ok
when ending the other sessions failed and `SetAdminPassword` doesn't bump the admin epoch;
clients - no final-path check on otc-sync's flash status file, no "could not be read" status on the
Mac, no `usePageRetry` in the web's Social feed or `DevicePhotoPicker`.

### Cases to print (`hardware/`)

Two OpenSCAD designs for the device (Pi 5 + Armor Lite V5 + the two status LEDs), under CC BY-SA
4.0 (`hardware/LICENSE.md`), not the AGPL: `hardware/cloud-case/` (`otc-case.scad`) and
`hardware/screen-case/` (`otc-screen-case.scad`, on a 75 x 75 mm M2.5 screen pattern, hung by two
keyholes). Every size is a named variable at the top of each source; the reference Pi model in each
(`pi5_ref`, with `g` to grow parts for clearances) is what the interference checks run against.
After any change run that folder's `render.sh` (OpenSCAD 2025+, Manifold): it rewrites the STLs
(committed, in printing orientation) and the previews, and runs the checks - each must come out
empty or zero-volume (contacts only); the READMEs describe every fit and must follow the source.
Both use one insert and one screw size (M2.5x4 heat-set inserts, M2.5 screws); keep the two cases'
shared parts (port cut-outs, LEDs, standoffs, front lid bosses) the same.

## Cutting a release (issue #94)

Devices update themselves in place: an owner presses **Update** in Settings and the
device applies every release it is missing, in order, then rebuilds its own binary and
restarts. Nothing is cross-compiled and no binaries are distributed — the device builds
from source, which is what keeps any architecture supported. The web app is the
exception: devices have no Node, so the bundle is built once, here, and attached to the
release.

**Cala is only ever updated this way.** Since 2026-09-23 Cala (installed from the image, SSH off)
gets every change through a release and the Update button in its Settings - never `make pi`/`make
web` against it - so the release history can't fall behind what devices actually run. Pit stays
the `make` development target. Cutting a release is the last step of any device or web change.

**How the button works** (release 5): otc.service runs unprivileged with `NoNewPrivileges=true`, so
the device cannot sudo. `updater.Apply` writes `/var/lib/otc/update.request`; systemd's
`otc-update.path` starts `otc-update.service`, which runs `/usr/local/bin/otc-update-runner` as
root: it reads `[otc] update-repo`/`update-releases` from the root-owned config, downloads
`scripts/update.sh` from there into `/run/otc-update/` and runs it. Those three files live in
`scripts/update-runner/` and are installed by install.sh and by release 5. A device without the
runner falls back to the old `sudo -n` path, which only works on a hand-set-up box like Pit; where
it can't - the service runs with NoNewPrivileges (every unit install.sh and Makefile.pi ever wrote)
or `sudo -n true` fails - Update now answers `dev.update.no_updater` ("This device was set up
without the updater ... run the README's install script on it again") instead of starting nothing
while the log said it had started (`legacyUpdaterWorks`, `updater/legacy_test.go`). To
exercise the whole flow on Pit without the app: `ssh otc@pit.otc touch /var/lib/otc/update.request`
and watch `/var/lib/otc/update-status.json`.
otc-update.service has `TimeoutStartSec=4h` (a migration must finish well inside it) and
`ExecStopPost` `otc-update-stopped`, which turns a status still at "running" into failed;
`updater.CurrentStatus` does the same, read-only, when the unit has stopped and nothing holds
`/run/otc-update.lock` (update.sh's shared flock) - power cuts included. Root status files are a
`dd conv=excl` temp file under umask 022, then `mv -T`: never `> file` or chmod as root in
`/var/lib/otc`, which `otc` owns. Downloads in the runner, update.sh, verified-install.sh and
`fetch_pinned` carry `curl --connect-timeout 30 --speed-limit 1 --speed-time 120`, cutting only a
stalled transfer (no `--max-time`). A release file (`github.com/<owner>/<repo>/releases/download/
<tag>/<name>`: the source archive, the web app, protoc, the quality model, ONNX Runtime) whose link
still fails is fetched once more through GitHub's API (`gh_api_fetch`: the release by tag into a
file - never a pipe, which would keep a cut-short answer ahead of curl's retry -, the asset's id by
name, an integer read with `python3 -I`, then the asset with `Accept: application/octet-stream`),
and is used, as before, only once its hash matches the signed manifest or its pin. The runner,
update.sh and verified-install.sh first retry the link for about a minute (`gh_fetch`: `--retry 6
--retry-all-errors`, 1 s doubling); install.sh's `fetch_pinned` and `fetch_optional` keep their own
retries (5 and 3, 2 s apart) before the API. On 2026-11-09 GitHub's links answered 504 for over ten minutes for some files and not
others while the API served them, and Cala's update to 177 failed with "could not download the
source of release 177". The same `gh_api_fetch` is in the runner, update.sh, verified-install.sh
and install.sh (`fetch_pinned`, `fetch_optional`), checked by `updater/gh_fetch_test.go`, which
also runs it against a stand-in curl. The source archive is fetched by the runner already
installed, so that download has it from the update after the one that brings it; update.sh's own
downloads (the web app, protoc, the model) run from the release being installed and have it at
once.
More root runners follow the same trigger-file shape, and update.sh reinstalls all of them on
every update (one that reads its request's words does so with `dd iflag=nofollow,nonblock`; each
removes whatever is at its request path with `rm -rf --` - a directory there, which `rm -f` can't
remove, had its `PathExists=` unit start it back to back for good; `-r` never follows a symlink;
`updater/runner_requests_test.go`; each
oneshot has a `TimeoutStartSec`, 2min for the bridge's, 3min for Tailscale's): `scripts/bridge-runner/`
(switching the bridge on later, #145),
`scripts/tailscale-runner/` (release 50) and `scripts/device-runner/` (release 115: `otc-reboot` for
Settings > Restart device and `otc-wifi-restart` for `wifiwatch`; release 115's script installs them
on older devices from `$OTC_VERIFIED_SRC`; release 164: `otc-poweroff` for Settings > Shut down
device, in the same loops, installed the first time by release 164's own update.sh - the runner
executes the update.sh of the verified release it installs - which removes a leftover
`/var/lib/otc/poweroff.request` before `enable --now`; release 140: `otc-wifi-join` for Settings › Wi-Fi, a
Python runner installed as `/usr/local/bin/otc-wifi-join-runner` with its own install lines rather
than the `reboot poweroff wifi-restart` loops, triggered by `/run/otc/wifi-join.request` - on the tmpfs, not
in `/var/lib/otc` - and answering in root's `/run/otc-wifi-join/status.json`; 140.sh installs it
with the restart runner, removes a stale request before `enable --now otc-wifi-join.path`, and
deletes the plaintext `/var/lib/otc/wifi_join_request.json` the retired SetWifi left).
Settings › Storage's `/run/otc/mirror-disk.request` (a disk that isn't blank, erased and added to
the mirror) has no unit of its own: `raid_watch.py`'s loop, which update.sh reinstalls, takes it the
same way and answers in root's `/run/otc-raid-watch/mirror-disk.json` (raidwatch above), deferring
a SIGTERM until it has answered. `otc-degraded.service` (issue #207, the device without its
storage) is written by install.sh and, on the devices installed before, by release 166's script -
the same `otc_storage_units` function (`updater/storage_units_test.go`), which also drops
`raid-watch.service`'s `After=multi-user.target` - and started only by `raid_watch.py`, which
update.sh reinstalls; a device rolled back to an older binary runs that one as a plain otc there
(waiting for its database), never touching the storage.
tailscaled runs only while Tailscale Funnel is on, and only root drives it (issue #204, release
168): install.sh and release 50 stop and disable it unless `tailscale funnel status` serves `:8080`.
Before 168 the otc user was its operator (release 2, install.sh, the runner), so the service could
do everything the CLI does - the account and tailnet, routes, exit nodes, serve, Tailscale SSH.
Now `tailscalefunnel` never runs the CLI: it writes one word of an allowlist to
`/var/lib/otc/tailscale.request` (temp file + rename, one request at a time) and
`otc-tailscale-runner` (root; the word read with `dd iflag=nofollow,nonblock`) runs the fixed
commands - `on`: `systemctl enable --now tailscaled`, the otc user's operator taken away (`tailscale
set --operator=`; when it can't be, the daemon is stopped again, `start_failed`), `tailscale up
--timeout=10s` for a node not in the tailnet once the daemon has settled (`Starting` is no answer),
then `tailscale funnel --bg --yes --https=443 localhost:8080`; `off`: `tailscale funnel reset` and
`systemctl disable --now tailscaled`; `status`: reads only (nothing at all while tailscaled is
stopped). The answer, `/var/lib/otc/tailscale-status.json`, names the word (`request`; a runner
from before names none) and carries what Settings shows - `logged_in`, `funnel_on`, `public_url`,
`login_url`, from `tailscale status --json --peers=false` (read with `python3 -I`) and `funnel
status`'s `:8080` - and Funnel's failures are runner codes (`join_failed`, `enable_failed`,
`disable_failed`, the CLI's words as `detail`, the key scrubbed). An auth key goes first, as
`/run/otc/tailscale-auth.key` (0600, the service's tmpfs): the runner takes it whatever the word,
renaming it into its unit's RuntimeDirectory (`/run/otc-tailscale`, 0700, gone after the run), uses
only a regular file of otc's with one link and at most 512 printable ASCII characters, and writes
its own copy for `--auth-key=file:` with bash's printf builtin - never a command line or a log.
`Status` keeps an answer 2 s (`cStatusFresh`) and, during an on or off, answers the last one
(`TryLock`); a word not taken within its wait (on 3 min 10 s, off 90 s, status 15 s) is withdrawn.
otc-tailscale.service has `StartLimitIntervalSec=0` (Settings runs it each time it shows) and
`TimeoutStartSec=3min`. `--drop-operator` takes away an operator that is the otc user (or can't be
read; one an owner chose for an account of theirs stays), starting a stopped daemon whose saved
state (`/var/lib/tailscale/tailscaled.state`) names otc just for that: install.sh runs it from the
verified source, and release 168's script installs the new runner first (the old one would set the
operator again during the update), then runs it, only warning. The otc user keeps what every local
account has on Linux, read-only `tailscale status`; `set`, `up`, `login`, `logout`, `cert` and the
serve config answer "Access denied". Tested on 2026-11-03 in a throwaway Lima VM (Debian 13,
tailscale 1.104.5) against a headscale 0.29.7 in the VM answering as `controlplane.tailscale.com`
(an `/etc/hosts` line and a test CA), so the product's own `tailscale up` signed in to it: release
167 installed by verified-install.sh signed in through Settings' request with a pre-auth key and made
otc the operator (otc changed the hostname); updating it through update.sh to this branch (168.sh in
the migrations loop) started the stopped daemon, cleared the operator and stopped it; then on/off,
status, a key sealed and plain, a key headscale refused, no key (the login page, approved in
headscale), Funnel's real error from headscale ("HTTPS must be enabled", in the request's language)
and its success through a stand-in for the three funnel commands, the key never in any process's
command line (sampled every 5 ms), the journal, the logs or `/run`; a symlink as the key, a FIFO as
the request, a word off the list and 15 requests in a row; install.sh re-run from the branch.

**Follow this whenever a change is worth shipping.** A change that is only on `main` has
not reached anybody: the version number in the manifest is the only signal a device has
that anything is new.

Issue #160: releases are **signed**. Cut one with `scripts/release.sh "Summary shown in
Settings"` from a clean, pushed `main` - nothing else. If the release needs a schema or
config change, first commit its script as `scripts/updates/<N>.sh` (N = the manifest's
last version + 1; it runs as root on the primary and on every per-user database and MUST
be idempotent; update.sh runs it before building the new binary, so it only sees rows the old code
wrote). A script that loops over users must let only a `db_name` matching `^otc_[0-9a-f]{32}$` reach
root's mysql command line (the otc service can write `users`, and a name starting with `-` would be
an option), as `92.sh` (one like per domain) does, and must stop when the list can't be read: a
query that fails is the database failing, not "no other users" (`169.sh`'s `dev_release_months`:
the table checked with `SHOW TABLES LIKE 'users'`, then the list and each database's existence read
strictly). Each change in a release's script is a section of its own, `# --- <area>: <what> ---`
(`169.sh`). The tool refuses anything uncommitted or untracked under `scripts/updates` (a script
whose hash was signed while it was missing from the archive stopped every device's update for
good), unpacks a `git archive` of HEAD, installs the web app's packages there from its lockfile
(`npm ci`), runs the i18n gate and builds the web bundle in that tree (never this checkout's files,
nor a `VITE_` variable of the shell), tags `vN`, makes
the release's own source archive (`src.tar.gz`, `git archive` + `gzip -n`; the manifests and their
signatures are `export-ignore`d in `.gitattributes`, since they carry its hash, and so are
`docs/app-store`, `docs/play-store`, `docs/screenshots` and `bridge/bin/otc_bridge` - about 11 MB
instead of 90; over 30 MB is refused), appends the manifest line (version, script sha - of the
script as that archive holds it, what devices check - web sha, summary, source sha) and signs the
whole manifest with the release key into `scripts/updates/VERSIONS.sig`. Then it pushes the tag, publishes the GitHub release, checks the
published archives, and only then commits and pushes the signed manifest to main - archives first,
manifest last. A failure before that commit deletes the release and the tag; a failed final push of
main must be finished by hand (`pull --rebase`, push). The key is Ed25519 at
`~/.otc/otc-release-signing.pem` on the Mac mini (passphrase in the Keychain item
`otc-release-signing`; never on GitHub - back it up offline); its public half is
`scripts/release-signing.pub`, which devices pin in `/etc/otc/release-signing.pub`. The passphrase
is never exported (release.sh, sign-file.sh): only the `openssl pkcs8` that decrypts the key gets
it - the build runs npm packages and Go programs, any of which could read an exported variable and
the key's fixed path.

**A released script is never edited**: its hash is line N of the signed manifest, and
`updater/released_scripts_test.go` holds every `scripts/updates/<N>.sh` to it. One found wrong
later gets a corrected copy, `scripts/updates/corrected/<N>.sh` (its `README.md` lists them):
update.sh checks the original against the manifest as always, then runs the corrected one in its
place, from the same verified source - so only a device still missing release N ever runs it.
Release 5's installs the update runner from the verified source (it downloaded it from the
branch, unsigned); 47's and 58's work on the logs and the storage as the otc user (root edited,
chowned and chmodded names in directories that user owns, and a symlink put there handed it the
target, or a copy of it); 158's makes the Image Tagging choice restartable (the column added with
no value, given one, then made NOT NULL - install.sh's block does the same); 161's is the system
updates' (above). Every release script runs through `run_release` (update.sh), with
`DEBIAN_FRONTEND=noninteractive` and update.sh's own `mysql`
(`scripts/update-runner/release-mysql.sh`, inherited as a function that runs it with bash, so a
temporary directory mounted noexec doesn't matter). For releases before 92 it lets only `-N`, `-B`,
`-e <statement>` and the database `otc` or an existing `otc_<32 hex>` reach the client - their
loops passed any users.db_name as mysql's first argument, where `--defaults-extra-file=` makes
root's client read a file of the otc user's - and skips anything else, as 92 onwards do. For every
release it notes a failed `SELECT db_name FROM users` other than error 1146 (no users table), and a
failed `SHOW DATABASES LIKE '<name>'` (92 onwards pipe it into grep, so it read as "no such
database"): the release runs again once MariaDB answers, then the update stops with
`users_unreadable` ("release N could not read this device's list of users - press Update to try
again"); either used to read as nothing to do - MariaDB restarted by a security update half-way
through the loop was enough - and the release was recorded with every other user's database left
behind. Tested by running the released scripts themselves against a stand-in client
(`updater/release_mysql_test.go`).
Then run `scripts/docker-release.sh` (the `container` paragraph above): the Docker image for that
release is built, tested and published from the Mac (about an hour cold, less once its cache is
warm); a failure there never touches the Pi release.

Issue #183: a release is **minor** (default), **major** (`--major`: security, stability or
durability) or **critical** (`--critical`: breaks compatibility with the bridge or the apps if not
installed, or a serious security fix), and has a `major.minor` version: a minor adds to the minor
number, a major or critical starts the next major. Release 84 is 1.0; earlier ones have no version
and show as builds. Kinds and versions are in `scripts/updates/RELEASES` (`<release> <kind>
<version>`), signed into `RELEASES.sig` by `release.sh` and checked by the device with Go's Ed25519
(`updater/kinds.go`) - a separate file because the runners read `VERSIONS` by position. The main
instance checks for updates by itself every hour (`updater.Watch`): a pending major or critical
update adds one `Update` notification per version (a critical one also pushes, once, with it), and `Status.update_alert` carries it to every
app; a critical one shows a banner in the web app, iOS and Android (and a line in the Mac app and
otc-sync) until it is installed. Settings shows "1.1 (build 85)" and badges pending releases.
`updater.Check` trusts `VERSIONS` only with its signature (a fork must sign its VERSIONS too) and
shows the last verified list on a mismatch (CDN skew); `Watch` keeps the known alert through a
failed check and retries from 5 min, doubling up to 6 h.

On a device, `otc-update-runner` downloads the manifest and its signature, verifies it with
the pinned key, downloads the target release's `src.tar.gz`, checks it against the signed
hash, and runs the `update.sh` inside it (`OTC_VERIFIED_MANIFEST`/`OTC_VERIFIED_SRC`): release
scripts come from that verified source and are checked against the signed manifest, the web
bundle too, and nothing is ever run from `main`. A device whose runner predates this ran
`update.sh` from `main` once: that copy does the same checks with the key embedded in it,
then hands over to the verified release (which installs the key and the new runner).
Forks publishing their own releases replace `scripts/release-signing.pub` with their key. Fresh
installs are verified the same way: `scripts/verified-install.sh` (the image ships it as
`/usr/local/bin/otc-verified-install` with the key in `/etc/otc/release-signing.pub`; by hand,
`curl …/verified-install.sh | sudo bash -s -- <name>`) checks the newest release's signature,
source archive and web bundle, and runs the `install.sh` inside that verified source
(`OTC_VERIFIED_SRC`, `OTC_VERIFIED_WEB`, `OTC_RELEASE_VERSION`); `install.sh` run any other way hands
over to it. The install copies that source instead of cloning, installs the release's web bundle
(no Node or npm on devices), pins Go, ONNX Runtime, protoc and the models by SHA-256
(`fetch_pinned`: into a mktemp directory beside the destination, then renamed; `/usr/local/models`
is root's, 0755, read by the service), and installs Tailscale from its apt repository with the key
pinned by hash. Root never makes, chowns or chmods a name inside a directory the otc user owns by
following it - a re-run (or the wizard's Recover, from a link an old device left on its disks)
used to chown a symlink planted at `/var/lib/otc/users` or the storage's `unencrypted/`, handing
its target (`/var/lib/dpkg/info`, say) to the otc user: `otc_dir` removes anything there that
isn't a directory, has the otc user make a missing one (`runuser`), hands over one root made before
with `chown -h`, and sets the mode as the otc user (`updater/install_dirs_test.go`, as root on
Linux). MariaDB's datadir, `/mnt/storage/mysql` in that same storage root, is set up by
`mariadb_datadir`: a symlink or a file there stops the install before MariaDB is stopped, and after
the stop root changes into the directory and works only on `.`, once it has checked that is the
datadir itself - the service keeps running through a re-run and can swap the name for a link at any
moment, and the copy of a fresh datadir wrote through such a link, giving its target the datadir's
owner and mode. A directory still to be seeded is made root's alone first and seeded only when
nothing in it is another account's (`updater/install_mariadb_test.go`, as root on Linux). OpenCV's
source build runs in a `mktemp -d` directory under the world-writable `/var/tmp`, its log inside -
kept, as the error points at it, when pkg-config can't find what the build installed
(`updater/install_opencv_test.go`) - and `/etc/otc/otc-install.env` is written under umask 077,
then renamed. A
failed read of the users table, or of whether a user's database exists, stops the schema block
(the container runs it too) instead of skipping every other user's database. A re-run keeps the config's `[otc]` lines it doesn't write itself (a fork's
`update-repo`/`update-releases`, `low-memory`, a watchdog switched off) and, without
`OTC_BRIDGE_ADDR`, the `bridge-addr`/`bridge-default` it finds (empty after a switch-off, #182); the
config is written temp (0640 root:otc), sync, rename. The device's MariaDB account holds ALL on `otc`
and, with GRANT OPTION, on `otc_%` (in a GRANT the `_` of `otc_<uuid>` is itself a wildcard: an
escaped `otc\_%` can't hand ALL on `otc_<uuid>` to that user's account - error 1044), plus CREATE
USER and RELOAD - nothing on `*.*` (install.sh, the container's entrypoint, release 143's REVOKE;
tested in the Lima VM by provisioning, measuring and dropping a user through `dao`).

**Checking it worked**: a device shows its version under Settings → Device version. The full
log of any run is at `/var/log/otc-update/update.log` (Settings › Logs › Updates), and the current
state is in `/var/lib/otc/update-status.json`. `/etc/otc/version` is written only once the whole
update has worked: migrations, build and web app. Release 88 on Pit showed why. GitHub answered 500
for its web bundle, the script carried on with the old one, and the per-release version write left
the device "up to date" for good. Each release's migration is recorded in
`/etc/otc/version.migrated`, so a failed run retries everything else without re-running scripts. A
web bundle that can't be downloaded fails the update, and a failed build leaves the running binary
untouched: update.sh runs migrations, build, then the web bundle's download and check, and only
then installs anything (`install_atomic`: temp, sync, rename) and writes the version file. The
staged release is removed on exit.
Since release 143 the update also checks the new version starts. The binary it replaces stays as
`/usr/bin/otc.prev` (a hard link), and the web app is unpacked into a directory of its own under
root's `/var/otc-web/` and switched in by renaming the `/var/www` symlink (the first time, the old
`/var/www` directory moves there): never written file by file into what is served, the one before
kept. After `systemctl restart otc` update.sh writes `verifying` and waits up to 3 minutes for the
service to answer `http://127.0.0.1:<[otc-api] port>/check_healty` and then keep answering for 20 s
as the same MainPID with no restart (Restart=on-failure alone never gives up on a binary that fails
at start). Otherwise it puts back the binary, the web app and `/etc/otc/version` (migrations and
version.migrated stay: they only add), restarts, and fails with `rolled_back`, or `rollback_failed`
if something couldn't be put back; on success the older web directories go. The legacy sudo path
runs inside otc.service, so it still reports done and restarts last, unchecked. Tested in the Lima VM
with a release whose binary exits at start. `/etc/otc/version` and version.migrated are written
temp, sync, rename (install.sh too, which writes both); a version file that holds no number (a
write cut short) is recovered from version.migrated by the runner and update.sh (and written back),
or the update fails with `version_unreadable`; `updater` reads it the same way and reports
`Info.VersionUnknown` (nothing pending, no alert) - read as 0, every release was announced. Before
downloading, the runner checks free space (150 MB on `/run/otc-update`, 1 GB on `/`) and update.sh
again (1 GB on `/`, 300 MB in its temporary directory) - `no_space`. A manifest whose signature
doesn't match is fetched again every 10 s for a minute (CDN lag right after a release) before
`manifest_not_signed`, worded "could not verify this release yet - … try again in a few minutes";
verified-install.sh retries the same way.

**Forks** set `[otc] update-repo` and `[otc] update-releases` so a device updates from its
own repository rather than silently taking code from upstream.

## Config

Runtime config is INI, loaded via `cfg.Init(appName, env)`: it reads `etc/otc_<env>.ini` relative to
the working directory, or `/etc/otc_<env>.ini` if that's missing. Dev config lives at
`cfg/etc/config_dev.ini`; see `README.md` for the full annotated `[otc]`, `[otc-api]`, `[mysql]`,
`[logger]`, `[tagger]`, `[faces]` sections used in production on the Pi.

## VERY IMPORTANT NOTES
- **The repository is public: commit only what outside readers are meant to read** (users,
  contributors, forks, the press), written for them. Plans, to-do lists, drafts, store-submission
  notes, GDPR records and notes about the maintainer's decisions go in `private/` (ignored by git:
  `private/stores/`, `private/gdpr/`, `private/press/`), never in `docs/` or anywhere else tracked.
- **The iOS and Android apps must stay in sync - this is critical.** `app/android` is a 1:1 port of
  `app/ios`: any change to a screen, flow, request, setting or behaviour in one app must be made in
  the other in the same piece of work, with the matching file/type name (SwiftUI view <->
  Composable, ViewModel <-> ViewModel). Never ship a feature or fix to only one of them; if the
  other side genuinely can't be done yet, open a GitHub issue for it before finishing.
- **The macOS, Windows and Linux sync clients must stay in sync too - equally critical.** The
  macOS app (`app/macos`, Swift) and `otc-sync` (`app/desktop`, Go, the Windows and Linux client)
  are the same product: any change to what one of them does - a menu item, a sync rule, a
  setting, a status, how it starts at login - is made to the other in the same piece of work
  (Swift file <-> Go package, see "Desktop sync client"). Never ship to only one of the three
  platforms; if a platform genuinely can't be done yet, open a GitHub issue for it before
  finishing.
- All the sensible content like photographies or files of any kind uploaded by the user should be encrypted at rest
- No sensible communications should be shared over unsecure channels
- Security is our main prioirty then reliability, durability and performance
- Every time that a make is done, update the documentation, installation scripts, images and any other necessary parts
- Test as much as possible in the browser or the iOS and Android simulators
- I'm working on other machine, so before working on something, do a `git pull` to update from what we have in the repo
