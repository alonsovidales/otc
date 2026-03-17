**Off The Cloud**
==============

OTC is a self hosted and inexpensive *NAS* solution to backup all your photos, videos and important documents, but it is also a ethical and safe *Social Network* free of toxic behaviours, harassment, doom scrolling, data harvesting, influencers... just a space to share memories with your family and friends that you own and you control.

OTC runs in your mobile devices as a iOS or Android application that can be download from the store (they are still not published). These apps will backup in background all the photos to the device in full resolution, we also have a MacOS and Windows client to backup and keep in sync folders in your computer.

OTC is hosted in your home using your network connection and inexpensive hardware, everything is designed to work on a Raspberry Pi 5 with 8GB of RAM and two MicroSD cards in a RAID 1 configuration to store the data. The estimated cost of all the necessary hardware for a 1TB device is under 250 euros.

**Recommended Hardware**

- 1x [Raspberry Pi 5 with 8GB or RAM](https://www.raspberrypi.com/products/raspberry-pi-5/)
- 2x USB MicroSD card readers
- 2x MicroSD Cards of the same size for storage
- 1x MicroSD card to host the OS in the RaspberryPi
- 1x [Power Supply](https://www.raspberrypi.com/products/27w-power-supply/) (Use something of at least 27W since the consumption is quite high when processing images)
- 1x [Active Cooler](https://www.raspberrypi.com/products/active-cooler/)

**Installation of the device**
1. Install [Raspberry Pi OS (64-bit)](https://www.raspberrypi.com/software/operating-systems/) in the Raspberry Pi using [this tutorial](https://www.raspberrypi.com/documentation/computers/getting-started.html#raspberry-pi-imager). In `Customisation` Select Enable SSH and use `otc` as user name.

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
model-path=/usr/local/models/ram_plus_swin_large_14m.onnx
tags-path=/usr/local/models/tag_list_4585.txt
tags-per-image=10
max-images-search=5
```

10. Download the models, from the repository directory:
```
$ cd models
$ python dowload.py
$ scp models/ram_plus/onnx/ram_plus_swin_large_14m.onnx otc@<otc_addr>:/usr/local/models/
$ python download_tags.py
$ scp ./models/ram_plus/tags/tag_list_4585.txt otc@<otc_addr>:/usr/local/models/
```

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
