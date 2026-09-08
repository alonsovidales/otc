#!/bin/bash
# Builds a distributable OTC image (issue #38) by customizing a stock
# Raspberry Pi OS Lite (64-bit) image: installs MariaDB + copies this
# device's own already-built/tested artifacts (otc binary, web bundle,
# ONNX runtime + tagging model, raid-watch/network-setup scripts) into it,
# then wires up first-boot provisioning (otc_firstrun.sh, in this same
# directory) so each flashed card gets its own fresh identity instead of
# cloning this device's.
#
# Run as root, ON a fully bootstrapped, working OTC device itself (e.g. one
# set up via `make -f Makefile.pi bootstrap`) — not on your own workstation.
# It reads that device's already-installed /usr/bin/otc, /var/www,
# /usr/local/models, and the raid-watch/network-setup services, and needs
# to run on the same CPU architecture as the image it's building (aarch64
# for a Raspberry Pi), since it chroots into the mounted image directly
# with no cross-arch/QEMU step.
#
#   $ sudo bash scripts/build_image.sh
#
# Produces $WORK/otc.img (uncompressed) — compress it yourself afterwards,
# e.g. `xz -T0 -k otc.img`, before publishing (see the README's "Option 1"
# install instructions for where released images are expected to live).
set -ex

WORK=${WORK:-/home/otc/image-build}
SRC_REPO=${SRC_REPO:-/home/otc/otc}
GROW_BY=${GROW_BY:-2G}

IMG_XZ=$WORK/base.img.xz
IMG=$WORK/otc.img
MNT=$WORK/mnt

mkdir -p "$WORK" "$MNT"

echo "=== [1/10] Download base image ==="
if [ ! -f "$IMG_XZ" ]; then
    curl -L -o "$IMG_XZ" https://downloads.raspberrypi.com/raspios_lite_arm64_latest
fi

echo "=== [2/10] Decompress (working copy — base stays untouched for re-runs) ==="
xz -dk -T0 -c "$IMG_XZ" > "$IMG"

echo "=== [3/10] Grow the image so there's room for the model/binaries we're adding ==="
# Stock image's root partition is sized tight to stock content.
truncate -s +"$GROW_BY" "$IMG"

echo "=== [4/10] Attach loop device, resize partition 2 to fill the new space ==="
LOOPDEV=$(losetup --show -fP "$IMG")
partprobe "$LOOPDEV" || true
sleep 1
parted -s "$LOOPDEV" resizepart 2 100%
partprobe "$LOOPDEV" || true
sleep 1
e2fsck -f -y "${LOOPDEV}p2" || true
resize2fs "${LOOPDEV}p2"

echo "=== [5/10] Mount root + boot ==="
mount "${LOOPDEV}p2" "$MNT"
mount "${LOOPDEV}p1" "$MNT/boot/firmware"

echo "=== [6/10] Bind mounts + resolv.conf for a working chroot ==="
mount --bind /dev "$MNT/dev"
mount --bind /proc "$MNT/proc"
mount --bind /sys "$MNT/sys"
cp /etc/resolv.conf "$MNT/etc/resolv.conf"

cleanup() {
    set +e
    umount "$MNT/boot/firmware" 2>/dev/null
    umount "$MNT/dev" 2>/dev/null
    umount "$MNT/proc" 2>/dev/null
    umount "$MNT/sys" 2>/dev/null
    umount "$MNT" 2>/dev/null
    losetup -d "$LOOPDEV" 2>/dev/null
}
trap cleanup EXIT

echo "=== [7/10] Install packages in chroot (mariadb, ffmpeg, onnxruntime) ==="
chroot "$MNT" /bin/bash -c "
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y mariadb-server ffmpeg libonnxruntime1.21
"

# cloud-init ships on stock Raspberry Pi OS to process Raspberry Pi
# Imager's own Customisation data (user-data/network-config on the boot
# partition) — this image never provides any, by design, since it
# provisions itself instead (otc-firstrun.service/network-setup.service).
# Found on real Pi 5 hardware that boot can stall reaching
# cloud-init.target regardless of the MariaDB/otc-firstrun fixes above —
# cloud-init's own datasource detection runs from a systemd *generator*
# (executes unconditionally, very early in boot, before any unit can be
# masked/disabled to stop it), so purging the package outright — removing
# the generator script itself — is the only way to be sure this can't
# happen, rather than trying to tune around whatever it's doing.
chroot "$MNT" /bin/bash -c "
    export DEBIAN_FRONTEND=noninteractive
    apt-get purge -y cloud-init
    apt-get autoremove -y
"

echo "=== [8/10] Create the otc service account ==="
chroot "$MNT" /bin/bash -c "
    id otc >/dev/null 2>&1 || useradd -r -m -d /home/otc -s /usr/sbin/nologin \
        -G dialout,video,plugdev,gpio,i2c,spi otc
"

# Masking userconfig.service below (stock Raspberry Pi OS's own first-boot
# "create a login user" prompt) closes the interactive-console-blocking
# hole it caused (issue #38 follow-up, found on real Pi 5 hardware) but
# also removes the *only* way to ever get a shell on the device — the
# service account above is deliberately non-interactive (-s
# /usr/sbin/nologin), and Imager's own customisation is skipped by design
# for this image. Without this, a stuck boot is completely undebuggable
# from the console. This is console-only (SSH isn't enabled by this image
# either), so it's gated by already having physical access to the device.
DEBUG_PASSWORD_HASH=$(openssl passwd -6 'off-the-cloud')
chroot "$MNT" /bin/bash -c "
    id otc-debug >/dev/null 2>&1 || useradd -m -s /bin/bash -G sudo otc-debug
    echo 'otc-debug:$DEBUG_PASSWORD_HASH' | chpasswd -e
"

echo "=== [9/10] Copy this device's own already-built/tested artifacts ==="
cp /usr/bin/otc "$MNT/usr/bin/otc"
chmod +x "$MNT/usr/bin/otc"

mkdir -p "$MNT/var/www"
cp -a /var/www/. "$MNT/var/www/"

mkdir -p "$MNT/usr/local/models"
cp -a /usr/local/models/. "$MNT/usr/local/models/"

cp "$SRC_REPO/scripts/raid_watch.py" "$MNT/usr/local/bin/raid_watch.py"
cp "$SRC_REPO/scripts/network_setup.py" "$MNT/usr/local/bin/network_setup.py"
chmod +x "$MNT/usr/local/bin/raid_watch.py" "$MNT/usr/local/bin/network_setup.py"

mkdir -p "$MNT/usr/local/share/otc"
cp "$SRC_REPO/db/db.sql" "$MNT/usr/local/share/otc/db.sql"

cp "$SRC_REPO/scripts/otc_firstrun.sh" "$MNT/usr/local/bin/otc_firstrun.sh"
chmod +x "$MNT/usr/local/bin/otc_firstrun.sh"

# systemd units: otc.service + its env file copied verbatim from this
# already-working device; raid-watch/network-setup are assumed already
# installed here too (via `make -f Makefile.pi raid-watch`/`network-setup`)
# so their unit files are copied straight from the live system rather than
# re-derived.
cp /etc/systemd/system/otc.service "$MNT/etc/systemd/system/otc.service"
cp /etc/systemd/system/raid-watch.service "$MNT/etc/systemd/system/raid-watch.service"
cp /etc/systemd/system/network-setup.service "$MNT/etc/systemd/system/network-setup.service"
mkdir -p "$MNT/etc/otc"
cp /etc/otc/otc.env "$MNT/etc/otc/otc.env"

cat > "$MNT/etc/systemd/system/otc-firstrun.service" <<'EOF'
[Unit]
Description=OTC first-boot provisioning
After=mariadb.service
Wants=mariadb.service
Before=otc.service raid-watch.service

[Service]
Type=oneshot
RemainAfterExit=yes
# This script has its own internal ~240s polling ceiling for MariaDB, but
# that's only a safety net *inside* the script — nothing previously bounded
# how long systemd itself would wait for this unit's job to finish. Found
# on real Pi 5 hardware that this mattered even with that internal limit:
# boot can sit showing "Job otc-firstrun.service/start running" with no
# console/login access at all until the job resolves one way or another.
# This is the actual guarantee that it always does, regardless of what
# otc_firstrun.sh is doing or why.
TimeoutStartSec=300
ExecStart=/usr/local/bin/otc_firstrun.sh

[Install]
WantedBy=multi-user.target
EOF

# otc.service and raid-watch genuinely need firstrun's DB/config done first
# — drop-ins rather than editing the unit files copied above verbatim.
# network-setup deliberately does NOT wait on this: it's what gets a
# person to the setup wizard in the first place (the WiFi AP), and must
# come up on its own regardless of whether firstrun (or MariaDB, or
# anything else) is still working or has hit a snag — the one real bug
# this fixes, found via a live test on real Pi 5 hardware where firstrun
# blocking network-setup meant the WiFi AP never appeared at all.
mkdir -p "$MNT/etc/systemd/system/otc.service.d" \
         "$MNT/etc/systemd/system/raid-watch.service.d"
for svc in otc raid-watch; do
    cat > "$MNT/etc/systemd/system/$svc.service.d/override.conf" <<'EOF'
[Unit]
After=otc-firstrun.service
Requires=otc-firstrun.service
EOF
done

echo "=== [10/10] Enable services + clean up for distribution ==="
chroot "$MNT" systemctl enable otc-firstrun.service
chroot "$MNT" systemctl enable otc.service
chroot "$MNT" systemctl enable raid-watch.service
chroot "$MNT" systemctl enable network-setup.service

# Stock Raspberry Pi OS's own first-boot flow prompts *interactively on the
# console* to create a user account when nothing satisfied it ahead of time
# (normally Raspberry Pi Imager's own "Customisation" step writes
# /boot/firmware/userconf.txt to pre-answer this) — since this image
# provisions itself instead (otc-firstrun.service) and was flashed without
# that step (see README), nothing was ever going to answer that prompt, so
# it just sat there blocking every later boot step — including
# network-setup and otc-firstrun themselves — waiting for a keyboard that
# usually isn't even connected. Masking it outright is correct here: we
# genuinely don't want an interactive Linux login account for this device
# at all, only the otc service account, which is already created above.
chroot "$MNT" systemctl mask userconfig.service 2>/dev/null || true

chroot "$MNT" apt-get clean
rm -rf "$MNT/var/lib/apt/lists/"*

# Fresh identity per flashed card, not this build machine's.
rm -f "$MNT"/etc/ssh/ssh_host_*
: > "$MNT/etc/machine-id"
rm -f "$MNT/var/lib/dbus/machine-id" 2>/dev/null || true

echo "Build complete. Image (pre-compression): $IMG"
ls -lh "$IMG"
