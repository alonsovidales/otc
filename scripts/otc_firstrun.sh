#!/bin/bash
# First-boot provisioning for an OTC image (issue #38).
#
# Runs once (gated by MARKER below) as a systemd oneshot before otc.service
# — see otc-firstrun.service. Everything here is the non-interactive
# equivalent of what Makefile.pi's secrets/db-schema/config targets do by
# hand over SSH for a manually-provisioned device: generate this device's
# own identity and DB password, load the schema, write the config, and
# start the real service for the first time.
set -e

MARKER=/var/lib/otc/first-boot-done
if [ -f "$MARKER" ]; then
    exit 0
fi

mkdir -p /var/lib/otc /var/log/otc /mnt/storage/unencrypted /etc/otc

DEVICE_UUID=$(cat /proc/sys/kernel/random/uuid)
BRIDGE_SECRET=$(openssl rand -hex 24)
OTC_DB_PASS=$(openssl rand -base64 24 | tr -d '=+/')
SUBDOMAIN="otc-$(echo "$DEVICE_UUID" | cut -c1-8).off-the.cloud"

echo "[otc-firstrun] provisioning device $DEVICE_UUID as $SUBDOMAIN"

# mariadb-server's own postinst never got to start it for real (it was
# installed inside a chroot during the image build, with no init system
# running) — this is the first time it actually comes up.
systemctl enable --now mariadb
for i in $(seq 1 30); do
    mysqladmin ping >/dev/null 2>&1 && break
    sleep 1
done

mysql -e "CREATE DATABASE IF NOT EXISTS otc;"
mysql -e "CREATE USER IF NOT EXISTS 'otc'@'localhost' IDENTIFIED BY '$OTC_DB_PASS';"
mysql -e "ALTER USER 'otc'@'localhost' IDENTIFIED BY '$OTC_DB_PASS';"
mysql -e "GRANT ALL PRIVILEGES ON otc.* TO 'otc'@'localhost';"
mysql -e "FLUSH PRIVILEGES;"

if mysql otc -N -B -e 'SHOW TABLES LIKE "files"' 2>/dev/null | grep -q files; then
    echo "[otc-firstrun] schema already present, skipping"
else
    echo "[otc-firstrun] loading schema"
    # Same skip as Makefile.pi's db-schema target: db.sql's first 9 lines
    # create a fixed dev-only user/password and drop/recreate the
    # database, which would stomp the real per-device user/password just
    # created above.
    tail -n +10 /usr/local/share/otc/db.sql | mysql otc
fi

mysql otc -e "
INSERT INTO settings (device_uuid, subdomain, bridge_secret)
SELECT '$DEVICE_UUID', '$SUBDOMAIN', '$BRIDGE_SECRET'
WHERE NOT EXISTS (SELECT 1 FROM settings);
"

cat > /etc/otc_dev.ini <<EOF
[otc]
bridge-addr=off-the.cloud
bridge-connections=5
storage-path=/mnt/storage/
unenc-storage-path=/mnt/storage/unencrypted/
max-thumbnail-width-px=1000
shared-link-ttl-hours=168

[logger]
log_file=/var/log/otc/otc.log
max_log_size_mb=10
level=info

[otc-api]
base-url=otc/
static=/var/www/
port=8080
ssl-port=443
ssl-cert=
ssl-key=

[mysql]
user=otc
pass=$OTC_DB_PASS
port=3306
db=otc

[tagger]
model-path=/usr/local/models/ram_plus_swin_large_14m.int8.onnx
tags-path=/usr/local/models/tag_list_4585.txt
thresholds-path=/usr/local/models/tag_list_4585_thresholds.txt
tags-per-image=10
max-images-search=5
EOF

chown otc:otc /var/log/otc /var/www /mnt/storage/ /mnt/storage/unencrypted 2>/dev/null || true

touch "$MARKER"
echo "[otc-firstrun] done — starting otc, raid-watch, network-setup"

systemctl enable --now otc.service
systemctl enable --now raid-watch.service
systemctl enable --now network-setup.service
