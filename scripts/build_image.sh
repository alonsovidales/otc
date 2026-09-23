#!/bin/bash
# Builds the flashable OTC image (issue #38): stock Raspberry Pi OS Lite
# (64-bit) plus the first-boot setup wizard (setup_wizard.py), the hotspot
# script (network_setup.py) and their systemd units - and nothing else.
# Everything a device actually runs is installed by scripts/install.sh,
# fetched fresh from GitHub by the wizard at setup time, so a new release
# of the software never needs a new image: the image is only how a person
# reaches the wizard.
#
# Run as root on any arm64 Linux box with losetup (a Raspberry Pi is fine;
# `make image` runs it on TARGET over SSH) - the chroot below only runs
# systemctl/apt, no binaries are copied in, so the host's own release
# doesn't matter.
#
#   $ make image TARGET=pit.otc          # from a dev machine
#   $ sudo bash scripts/build_image.sh   # or directly on the device
#
# Produces, under $WORK:
#   off-the-cloud-rpi-lite-arm64.img.xz         the image (~550MB)
#   off-the-cloud-rpi-lite-arm64.img.xz.sha256  its checksum
set -euo pipefail

WORK=${WORK:-/home/otc/image-build}
SRC_REPO=${SRC_REPO:-/home/otc/otc}
BASE_URL=${BASE_URL:-https://downloads.raspberrypi.com/raspios_lite_arm64_latest}
XZ_LEVEL=${XZ_LEVEL:--6}
NAME=${NAME:-off-the-cloud-rpi-lite-arm64}
# The hostname the image boots with: with mDNS that makes the wizard
# reachable at http://otc.local/ on a wired network, no hotspot needed.
HOSTNAME=${HOSTNAME_IN_IMAGE:-otc}

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo bash $0)"; exit 1; }
for f in "$SRC_REPO/scripts/setup_wizard.py" "$SRC_REPO/scripts/network_setup.py"; do
    [ -e "$f" ] || { echo "missing $f - sync the repo to $SRC_REPO first (make sync)"; exit 1; }
done
for t in losetup partprobe xz sha256sum curl; do
    command -v "$t" >/dev/null || { echo "missing tool: $t"; exit 1; }
done

IMG_XZ=$WORK/base.img.xz
IMG=$WORK/$NAME.img
MNT=$WORK/mnt
mkdir -p "$WORK" "$MNT"

echo "=== Building $NAME ==="

echo "=== [1/7] Download base image (cached in $IMG_XZ) ==="
# Downloaded to a temporary name and renamed only once complete, so an
# interrupted download can't be mistaken for the cached base next time.
if [ ! -f "$IMG_XZ" ]; then
    curl -fL --retry 3 -o "$IMG_XZ.part" "$BASE_URL"
    mv "$IMG_XZ.part" "$IMG_XZ"
fi

echo "=== [2/7] Decompress (working copy - the base stays untouched for re-runs) ==="
rm -f "$IMG" "$IMG.xz"
xz -dk -T0 -c "$IMG_XZ" > "$IMG"

echo "=== [3/7] Mount ==="
LOOPDEV=$(losetup --show -fP "$IMG")
partprobe "$LOOPDEV" || true
sleep 1

cleanup() {
    set +e
    rm -f "$MNT/usr/sbin/policy-rc.d" "$MNT/zero" 2>/dev/null
    umount "$MNT/boot/firmware" 2>/dev/null
    umount "$MNT/dev/pts" 2>/dev/null
    umount "$MNT/dev" 2>/dev/null
    umount "$MNT/proc" 2>/dev/null
    umount "$MNT/sys" 2>/dev/null
    umount "$MNT" 2>/dev/null
    losetup -d "$LOOPDEV" 2>/dev/null
}
trap cleanup EXIT

mount "${LOOPDEV}p2" "$MNT"
mount "${LOOPDEV}p1" "$MNT/boot/firmware"
mount --bind /dev "$MNT/dev"
mount --bind /dev/pts "$MNT/dev/pts"
mount --bind /proc "$MNT/proc"
mount --bind /sys "$MNT/sys"
# No service may start inside the chroot (there is no init to start it
# under); package maintainer scripts respect this file.
printf '#!/bin/sh\nexit 101\n' > "$MNT/usr/sbin/policy-rc.d"
chmod +x "$MNT/usr/sbin/policy-rc.d"
cp /etc/resolv.conf "$MNT/etc/resolv.conf"

# mdadm is what the wizard needs before install.sh has run to find the
# RAID1 array (and the database on it) a dead Pi left behind, so the
# person is offered a recovery instead of a fresh name and a wipe. avahi
# is what makes http://otc.local/ resolve once the person is back on
# their own network after the WiFi step took the hotspot down. dnsmasq is
# what NetworkManager runs DHCP + the captive-portal DNS with on the
# hotspot.
chroot "$MNT" /bin/bash -c "
    set -e
    export DEBIAN_FRONTEND=noninteractive
    apt-get update
    apt-get install -y --no-install-recommends mdadm avahi-daemon dnsmasq-base
    systemctl enable avahi-daemon.service
"

echo "=== [4/7] Scripts, units, hostname ==="
install -m 0755 "$SRC_REPO/scripts/setup_wizard.py"  "$MNT/usr/local/bin/setup_wizard.py"
install -m 0755 "$SRC_REPO/scripts/network_setup.py" "$MNT/usr/local/bin/network_setup.py"

cat > "$MNT/etc/systemd/system/network-setup.service" <<'EOF'
[Unit]
Description=OTC first-boot WiFi/AP setup
After=NetworkManager.service
Wants=NetworkManager.service

[Service]
Type=simple
ExecStart=/usr/bin/python3 /usr/local/bin/network_setup.py
Restart=on-failure
User=root

[Install]
WantedBy=multi-user.target
EOF

cat > "$MNT/etc/systemd/system/otc-setup.service" <<'EOF'
[Unit]
Description=OTC first-boot setup wizard (issue #38)
After=network-setup.service NetworkManager.service
Wants=network-setup.service
# install.sh creates this at the end of a successful install; from then
# on the wizard has nothing to do and must not hold port 80 from otc.
ConditionPathExists=!/etc/otc/.install-complete

[Service]
Type=simple
ExecStart=/usr/bin/python3 /usr/local/bin/setup_wizard.py
Restart=on-failure
RestartSec=3
User=root

[Install]
WantedBy=multi-user.target
EOF

# NetworkManager's own WiFi switch ships off on stock Raspberry Pi OS
# (Imager/raspi-config turn it on when a country is set); network_setup.py
# turns it on at runtime too, this just makes the first boot start right.
mkdir -p "$MNT/var/lib/NetworkManager"
printf '[main]\nNetworkingEnabled=true\nWirelessEnabled=true\nWWANEnabled=true\n' > "$MNT/var/lib/NetworkManager/NetworkManager.state"

echo "$HOSTNAME" > "$MNT/etc/hostname"
sed -i "s/^127\.0\.1\.1.*/127.0.1.1\t$HOSTNAME/" "$MNT/etc/hosts"
grep -q "^127.0.1.1" "$MNT/etc/hosts" || printf '127.0.1.1\t%s\n' "$HOSTNAME" >> "$MNT/etc/hosts"

echo "=== [5/7] Enable/disable services in the chroot ==="
chroot "$MNT" systemctl enable network-setup.service otc-setup.service
# Stock Raspberry Pi OS's own first-boot flow prompts *interactively on the
# console* to create a user account when nothing pre-answered it (Imager's
# Customisation step normally writes /boot/firmware/userconf.txt). This
# image is flashed without that step, so the prompt would sit there
# blocking every later boot step, waiting for a keyboard that usually
# isn't even connected.
chroot "$MNT" systemctl mask userconfig.service 2>/dev/null || true
# That prompt is also what hands the HDMI console over to a login prompt
# once it's done (found on real hardware: with it masked, boot finished
# with no login on tty1 at all - only the serial console got one). Enable
# the console login directly instead, so the otc-debug account below is
# actually reachable from a keyboard and monitor.
chroot "$MNT" systemctl enable getty@tty1.service
# cloud-init ships on stock Raspberry Pi OS to process Imager's
# Customisation data, which this image never has. Found on real Pi 5
# hardware that boot can stall reaching cloud-init.target; its datasource
# detection runs from a systemd generator that no unit can be masked
# ahead of, so purging the package is the only sure way.
chroot "$MNT" /bin/bash -c "
    export DEBIAN_FRONTEND=noninteractive
    apt-get purge -y cloud-init 2>/dev/null || true
    apt-get autoremove -y 2>/dev/null || true
    apt-get clean
"
# A console login for troubleshooting a boot that never reaches the
# wizard: the only interactive account on the image, console-only (SSH is
# not enabled), so it's gated by physical access - and physical access to
# an unencrypted card is the whole game anyway. install.sh creates the
# unprivileged otc service account itself later.
DEBUG_PASSWORD_HASH=$(openssl passwd -6 'off-the-cloud')
chroot "$MNT" /bin/bash -c "
    id otc-debug >/dev/null 2>&1 || useradd -m -s /bin/bash -G sudo otc-debug
    echo 'otc-debug:$DEBUG_PASSWORD_HASH' | chpasswd -e
"

echo "=== [6/7] Clean up for distribution ==="
rm -rf "$MNT/var/lib/apt/lists/"*
rm -f "$MNT/usr/sbin/policy-rc.d"
# Fresh identity per flashed card, not this build machine's.
rm -f "$MNT"/etc/ssh/ssh_host_*
# ...which leaves sshd unable to start ("no hostkeys available") if the
# owner ever enables SSH from the console. The stock regenerate service
# isn't enabled on this image, so the keys are made on demand instead:
# ssh-keygen -A only creates what is missing, so it's a no-op afterwards.
# ExecStartPre is additive across drop-ins and runs in order, so the
# packaged `sshd -t` (which fails without keys) has to be cleared and
# re-added after the keygen.
mkdir -p "$MNT/etc/systemd/system/ssh.service.d"
cat > "$MNT/etc/systemd/system/ssh.service.d/hostkeys.conf" <<'EOF2'
[Service]
ExecStartPre=
ExecStartPre=/usr/bin/ssh-keygen -A
ExecStartPre=/usr/sbin/sshd -t
EOF2
: > "$MNT/etc/machine-id"
rm -f "$MNT/var/lib/dbus/machine-id"
# Zero the free space so it compresses to nothing.
dd if=/dev/zero of="$MNT/zero" bs=4M status=none 2>/dev/null || true
rm -f "$MNT/zero"
sync
cleanup
trap - EXIT

echo "=== [7/7] Compress ==="
xz -T0 "$XZ_LEVEL" "$IMG"
( cd "$WORK" && sha256sum "$NAME.img.xz" > "$NAME.img.xz.sha256" )

echo "Build complete:"
ls -lh "$WORK/$NAME.img.xz"
cat "$WORK/$NAME.img.xz.sha256"
