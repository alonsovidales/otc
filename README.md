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
2. Clone the repository: ```git clone git@github.com:alonsovidales/otc.git```
3. Edit the [MakeFile](https://github.com/alonsovidales/otc/blob/main/makefile#L11) and specify in `TARGET` the IP Address or hostname used by the Raspberry Pi
4. Connect to the device and execute the next in order to register the service:
```
sudo mkdir -p /etc/otc
sudo bash -c 'cat >/etc/otc/otc.env <<EOF
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

# Start command (adapt flags as needed)
ExecStart=/usr/bin/otc --config /etc/otc/config.yaml

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

# If you write to /var/lib/otc, allow it explicitly
ReadWritePaths=/var/lib/otc

[Install]
WantedBy=multi-user.target
UNIT

sudo systemctl daemon-reload
sudo systemctl enable --now otc.service
```
6. Execute the next in order to create the RAID1:
```
sudo wipefs -a /dev/sda
sudo wipefs -a /dev/sdb
sudo mdadm --create --verbose /dev/md0 --level=1 --raid-devices=2 /dev/sda /dev/sdb
sudo mkfs.ext4 /dev/md0
sudo mkdir /mnt/storage
sudo mount /dev/md0 /mnt/storage
sudo mdadm --detail --scan >> /etc/mdadm/mdadm.conf
sudo update-initramfs -u
# Add to /etc/fstab if you want it mounted automatically:
/dev/md0   /mnt/storage   ext4   defaults   0   0
```
7. Install MySQL and set the datadir to use the RAID:
```
```
8. Build the database:
```
```
9. Create the OTC config file:
```
```
10. In your computer, in the repository directory execute: `make all`, this will compile nd copy all the content to the device, note that you need [Go installed](https://go.dev/doc/install). Everytime that you want to change something and re-compile, this is the step to run
