**Off The Cloud**
==============

OTC is a self hosted and inexpensive *NAS* solution to backup all your photos, videos and important documents, but it is also a ethical and safe *Social Network* free of toxic behaviours, harassment, doom scrolling, data harvesting, influencers... just a space to share memories with your family and friends that you own and you control.

OTC runs in your mobile devices as a iOS or Android application that can be download from the store (they are still not published). These apps will backup in background all the photos to the device in full resolution, we also have a MacOS and Windows client to backup and keep in sync folders in your computer.

OTC is hosted in your home using your network connection and inexpensive hardware, everything is designed to work on a Raspberry Pi 5 with 8GB of RAM and two MicroSD cards in a RAID 1 configuration to store the data. The estimated cost of all the necessary hardware for a 1TB device is under 250 euros.

**OTC is composed by four systems:**
====================================

**The "device"**

This is the Raspberry Pi with two micro-SDs and two custom status leds that is used to store all your data and two custom LEDs to show the status of the RAID

<img height="300" alt="Untitled" src="https://github.com/user-attachments/assets/25f5ccfb-b1fe-4440-830c-f4d34e9c3881" />


**The MacOS/Windows application**

This is used to sync some folders in your local computer

<img height="300" alt="Screenshot 2026-05-18 at 16 07 21" src="https://github.com/user-attachments/assets/c5fcc06f-3df4-484d-a6f9-6cbfa0a6f363" />


**The iOS/Android app**

This is used to access all the data, sync photos and documents from your mobile device, Social Network app and much more

<img height="300" alt="3014CBED-F750-4949-A15E-6D01140833BF_1_102_o" src="https://github.com/user-attachments/assets/f147bfd7-7115-4e41-921c-4c59c3fa75a2" />
<img height="300" alt="F3DEA941-1E34-4645-851A-42388CF29A49_1_102_o" src="https://github.com/user-attachments/assets/8c86bb84-255b-43dd-a726-ae4faaff69eb" />
<img height="300" alt="58FC9CDB-06BC-4FA3-AEC0-AB0D6BBA8912_1_102_o" src="https://github.com/user-attachments/assets/957194a4-cdb0-4f0f-8018-3f971086d36e" />
<img height="300" alt="1A8E46EC-2AE1-40F2-B9BF-3C720A62DB68_1_102_o" src="https://github.com/user-attachments/assets/30549feb-bd38-47ad-b886-6d7af8db6e2b" />


**The Web app**

With this you can access all your data and social network from any browser just with your password

<img height="300" alt="Screenshot 2026-05-18 at 16 35 47" src="https://github.com/user-attachments/assets/f20f2e79-0385-4cdc-b955-48e9d3c44619" />
<img height="300" alt="Screenshot 2026-05-18 at 16 44 21" src="https://github.com/user-attachments/assets/08a7d890-33ec-4f67-8772-a8ed574baacf" />
<img height="300" alt="Screenshot 2026-05-18 at 16 45 16" src="https://github.com/user-attachments/assets/5758ff5d-5a89-4494-a633-a8da14cc4c5b" />

**Recommended Hardware**
========================
- 1x [Raspberry Pi 5 with 8GB or RAM](https://www.raspberrypi.com/products/raspberry-pi-5/)
- 2x USB MicroSD card readers
- 2x MicroSD Cards of the same size for storage
- 1x MicroSD card to host the OS in the RaspberryPi
- 1x [Power Supply](https://www.raspberrypi.com/products/27w-power-supply/) (Use something of at least 27W since the consumption is quite high when processing images)
- 1x [Active Cooler](https://www.raspberrypi.com/products/active-cooler/)

**Installation of the device**
==============================
1. Install [Raspberry Pi OS (64-bit)](https://www.raspberrypi.com/software/operating-systems/) in the Raspberry Pi using [this tutorial](https://www.raspberrypi.com/documentation/computers/getting-started.html#raspberry-pi-imager). In `Customisation` select Enable SSH, use `otc` as the user name, and enable passwordless sudo for it (the default for the account created there).

From here you have two options: let `Makefile.pi` do the rest automatically (recommended), or follow the numbered manual steps below yourself.

**Automated installation (`Makefile.pi`)**
-------------------------------------------
`Makefile.pi`, in the root of this repository, does everything from step 2 onwards on its own: builds the RAID1 array, installs and configures MariaDB, installs Go and ONNX Runtime, exports the RAM++ tagging model directly on the device, loads the database schema, writes the app config and systemd service, and finally builds and deploys the app itself. Run it from your computer (not the Pi), with the repository checked out:

```
$ make -f Makefile.pi bootstrap TARGET=<device_addr_or_hostname> DISK1=/dev/sda DISK2=/dev/sdb
```

A few things worth knowing before you run it:
- It only wipes `DISK1`/`DISK2` after showing you `lsblk` output and asking you to type `yes` to confirm - double check those are the right two disks before confirming. If a RAID1 array already exists on the device it skips the wipe automatically (pass `FORCE=1` to rebuild it from scratch).
- The RAM++ model export step runs the full torch/transformers pipeline on the Pi itself, so budget real time and bandwidth for it (a multi-GB download).
- The device's generated secrets (DB password, device UUID, bridge secret) are written to a local `.env.pi` file the first time you run it - keep that file, don't commit it, and don't lose it, since it's the only place the DB password is recorded.
- Re-running `bootstrap` (or any individual target) is safe; most steps detect what's already been done and skip it.

Once bootstrapped, day-to-day use is just:

```
$ make -f Makefile.pi deploy   # build the current code and (re)start the service on the device
$ make -f Makefile.pi status   # service / DB / RAID / healthcheck status
$ make -f Makefile.pi logs     # tail the service logs
```

Run `make -f Makefile.pi help` for the full list of targets (e.g. to re-run just `raid`, `mariadb`, `models`, or `onnxruntime` if one step needs retrying). Wiring the RAID status LEDs to the GPIO pins is still a manual, physical step - see step 4 below for the pinout.

The rest of this section documents what `Makefile.pi` does under the hood, step by step - useful if you want to customize the install, understand what changed on the device, or finish the job by hand if a step fails.

**Manual installation (step by step)**
---------------------------------------
2. SSH into the device and update the OS:
```
$ sudo apt-get update
$ sudo apt-get upgrade
```

3. Execute the next in order to create the RAID1:
```
$ sudo wipefs -a /dev/sda
$ sudo wipefs -a /dev/sdb
$ sudo mdadm --create --verbose /dev/md0 --level=1 --raid-devices=2 /dev/sda /dev/sdb
$ sudo mkfs.ext4 /dev/md0
$ sudo mkdir /mnt/storage
$ sudo mount /dev/md0 /mnt/storage
$ sudo mdadm --detail --scan >> /etc/mdadm/mdadm.conf
$ sudo update-initramfs -u

# Add to /etc/fstab if you want it mounted automatically:
/dev/md0   /mnt/storage   ext4   defaults   0   0
```

4. Add the RAID monitorig service
Create `/etc/systemd/system/raid-watch.service` with:
```
[Unit]
Description=RAID1 watcher + LED driver
After=multi-user.target mdadm.service

[Service]
Type=simple
ExecStart=/usr/bin/python3 /usr/local/bin/raid_watch.py
Restart=on-failure
User=root

[Install]
WantedBy=multi-user.target
```
then:
```
# From the local repo directory:
$ scp scripts/raid_watch.py otc@<device_addr>:/tmp/
# Connect by SSH to the device
$ sudo mv /tmp/raid_watch.py /usr/local/bin/raid_watch.py
$ sudo chmod +x /usr/local/bin/raid_watch.py
$ sudo systemctl daemon-reload
$ sudo systemctl enable --now raid-watch.service
```
For the status leds to work, you have to connect them to the GPIO ports as in: https://github.com/alonsovidales/otc/blob/1dec544957b5e41a49b99933cb6b5ba55ebf5ce5/scripts/raid_watch.py#L47-L52

You can use 3mm Red & Green LED Diode Light like: https://www.amazon.nl/-/en/dp/B01CFZMSNO

4. Install MariaDB and set the datadir to use the RAID:
```
$ sudo apt-get install mariadb-server
$ sudo mkdir /mnt/storage/mysql
$ sudo rsync -aHAX --numeric-ids --info=progress2 /var/lib/mysql/ /mnt/storage/mysql
```
Edit `/etc/mysql/mariadb.conf.d/50-server.cnf` and replace:
```
#datadir                 = /var/lib/mysql
```
by:
```
datadir                 = /mnt/storage/mysql
```
start MariaDB and check that the directory is properly set:
```
$ sudo systemctl start mariadb
$ sudo mysql -uroot -p -e "SHOW VARIABLES LIKE 'datadir';"
```
Populate the DB with the content from `db/db.sql`
Add the settings row that will be used to identify the device and connect to the bridge:
```
insert into settings (`device_uuid`, `subdomain`, `bridge_secret`) values ('<device_uuid>', '<device_domain>.off-the.cloud', '<device_secret>')
```
You can put random values there if you don't plan to use the bridge, but if you want your device to be remotely accesible, send us an email to: `avidales@off-the.cloud` and we will add your device. By the moment we only grant access to contributors, we will open the bridge to the pubic when the project is considered stable.

5. Edit the [MakeFile](https://github.com/alonsovidales/otc/blob/main/makefile#L11) and specify in `TARGET` the IP Address or hostname used by the Raspberry Pi

6. Create the `www` directory and install Go (use the latest version for Linux ARM64):
```
$ wget https://go.dev/dl/go1.26.1.linux-arm64.tar.gz
$ sudo tar -C /usr/local -xzf go1.26.1.linux-arm64.tar.gz
$ echo "export PATH=\$PATH:/usr/local/go/bin" >> .bash_profile
```

7. In your local machine, clone the repository and make the project:
```
$ git clone git@github.com:alonsovidales/otc.git
$ cd otc
# Edit makefile and replace TARGET by the address or hostname of the device
$ make all
```

8. Build the database:
```
$ sudo mysql -u root
> create database otc;
> CREATE USER 'otc'@'localhost' IDENTIFIED BY '<your_pass_here>';
> GRANT ALL PRIVILEGES ON otc.* TO 'otc'@'localhost';
> exit
```

9. Create the OTC config file in `/etc/otc_dev.ini` like:
```
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
level=debug

[otc-api]
base-url=otc/
static=/var/www/
port=8080
ssl-port=443
ssl-cert=
ssl-key=

[mysql]
user=otc
pass=<your_password_here>
port=3306
db=otc

[tagger]
model-path=/usr/local/models/ram_plus_swin_large_14m.int8.onnx
tags-path=/usr/local/models/tag_list_4585.txt
thresholds-path=/usr/local/models/tag_list_4585_thresholds.txt
tags-per-image=10
max-images-search=5
```

10. Download the models. `thresholds-path` is optional (older/from-scratch
    exports have no threshold file — drop that line if you go with the
    `models-export` fallback further below); when set, RAM++'s own per-tag
    calibrated cutoffs are used instead of one flat threshold for all 4585
    tags, which is more accurate. From the repository directory:
```
$ cd models
$ curl -fL -o ram_plus_swin_large_14m.int8.onnx https://huggingface.co/anakhiu/ram-plus-onnx-int8/resolve/main/ram_plus_int8.onnx
$ curl -fL -o tag_list_4585_thresholds.txt https://huggingface.co/anakhiu/ram-plus-onnx-int8/resolve/main/ram_tag_list_threshold.txt
$ scp ram_plus_swin_large_14m.int8.onnx tag_list_4585_thresholds.txt tag_list_4585.txt otc@<otc_addr>:/usr/local/models/
```
    (This is a community-hosted INT8 re-export of the same RAM++ Swin-Large
    weights and 4585-tag vocabulary used below — same tags, ~2-3x faster,
    half the size, no measurable accuracy loss in testing. If it's ever
    unavailable, `make -f Makefile.pi models-export` exports the original
    fp32 model from scratch instead — see that target for the manual
    equivalent, which needs a Python/torch/transformers toolchain and takes
    much longer.)

11. Install ONNX runtime:
```
$ wget https://github.com/microsoft/onnxruntime/releases/download/v1.24.3/onnxruntime-linux-aarch64-1.24.3.tgz
$ tar -xzf onnxruntime-linux-aarch64-1.24.3.tgz
$ sudo mv onnxruntime-linux-aarch64-1.24.3 /opt/onnxruntime
```

12. In your computer, in the repository directory execute: `make all`, this will compile nd copy all the content to the device, note that you need [Go installed](https://go.dev/doc/install). Everytime that you want to change something and re-compile, this is the step to run

13. Connect by SSH to the device and execute the next in order to register the service:
```
$ sudo mkdir -p /var/log/otc
$ sudo chown otc:otc /var/log/otc
$ sudo chmod 755 /var/log/otc

$ sudo mkdir -p /etc/otc
$ sudo bash -c 'cat >/etc/otc/otc.env <<EOF
OTC_LOG=info
OTC_ADDR=:8080
EOF'
sudo tee /etc/systemd/system/otc.service >/dev/null <<'UNIT'
[Unit]
Description=Off The Cloud service
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=otc
Group=otc
# If you want a writable working dir at runtime:
WorkingDirectory=/var/lib/otc

# Load environment variables (optional)
EnvironmentFile=-/etc/otc/otc.env

# Start command as dev, the cofig is in: /etc/otc_dev.ini
ExecStart=/usr/bin/otc dev

# Restart policy
Restart=on-failure
RestartSec=3

# Resource & fd limits (tweak to your needs)
LimitNOFILE=65535

# Runtime directories (systemd creates them with proper perms)
RuntimeDirectory=otc
StateDirectory=otc
LogsDirectory=otc

# Security hardening (safe defaults; relax if needed)
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
UNIT

$ sudo systemctl daemon-reload
$ sudo systemctl enable --now otc.service
```

14. If everything went well, you should be able to see the process running with:
```
$ journalctl -u otc -f
```
and the logs in:
```
$ tail -f /var/log/otc/otc.log
```
To connect locally use the `8080` port: http://<local_ip>:8080

If you have configured the bridge you should be able to connect in: https://<domain>.off-the.cloud/

**When clicking in "Sign In" it will ask you for a password, be careful because the first time, sice the password is not set, whatever you set will be your password.**

## Bridge admin panel

The bridge (`bridge/`, deployed separately - see `bridge/makefile`) has a small admin panel at
`https://off-the.cloud/admin` for whoever operates the bridge: log in, see/add/remove registered
devices, and check per-device metrics (requests/bandwidth, hourly) and a security log of rejected
bridge-registration attempts (wrong owner/secret for a claimed domain).

There's no sign-up - bootstrap (or change) an admin account from the bridge's shell:
```
$ sudo /usr/bin/otc_bridge <env> set-admin-password <username> <password>
```
`[admin] session-secret` must also be set in the bridge's config file (`/etc/otc_<env>.ini`) - a
random value that stays stable across restarts, e.g. `openssl rand -hex 32` - otherwise every
restart logs every admin out.
