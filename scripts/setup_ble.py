#!/usr/bin/env python3
# -*- coding: utf-8 -*-
# SPDX-License-Identifier: AGPL-3.0-or-later
"""
First-boot setup over Bluetooth (issue #137).

The setup wizard (setup_wizard.py) is a web page on port 80 that a phone
reaches over the "Off The Cloud" hotspot. This daemon offers the very same
wizard over Bluetooth LE, so the iOS and Android apps can set a new device
up without leaving the phone's own WiFi: the app shows the wizard's page
in a web view and every request that page makes (the page itself,
/api/state, /api/wifi, ...) travels over one GATT service to this daemon,
which forwards it to the wizard on 127.0.0.1 and streams the answer back.
Nothing is duplicated: the steps, the checks and the wording are the
wizard's, and whatever the wizard learns to do next works over Bluetooth
too.

GATT service (all UUIDs share the 0f7c5e70-0b1e-4b8a-9c2d-5e7a1c0d---- base):
  ...0001  the service, advertised under the name "Off The Cloud setup"
  ...0002  request   write / write-without-response: chunks of a request
  ...0003  response  notify: chunks of the answers
  ...0004  info      read: {"v": 1, "name": "<hostname>"}

Framing, both directions: byte 0 is a stream id the phone picks per
request, byte 1 is flags (bit 0: last chunk), the rest is payload; a
message is the payload of its chunks in order. A request is UTF-8 JSON
{"m": "GET"|"POST", "p": "/api/state", "b": "<body, may be empty>"}; an
answer is raw DEFLATE (no zlib header - what iOS's Compression framework
and Android's Inflater(nowrap) both take as-is) of JSON {"s": <status>,
"t": "<content type>", "b": "<body text>"}. Chunks are MTU-3 bytes, the MTU
being what BlueZ reports with each write, capped at 512 - the most an
attribute value can hold, whatever the MTU: BlueZ negotiates 517 with an
iPhone and a 514-byte notification arrives clipped to 512, which is how
the page first reached a phone 2 bytes short per chunk. A request may
also carry "c", the chunk size the phone would rather have.

Like the hotspot, this is deliberately open: it only exists on a device
that has not been set up yet (the unit has the same ConditionPathExists
as the wizard's) and it stops the moment the wizard marks the setup done,
the same marker network_setup.py drops the hotspot on.

Needs python3-dbus and python3-gi (baked into the image next to bluez).
`--selftest` exercises the framing and the forwarding against a local
stand-in for the wizard and needs neither.
"""
import json
import os
import socket
import sys
import threading
import time
import urllib.error
import urllib.request
import zlib
from pathlib import Path

sys.stdout.reconfigure(line_buffering=True)

CONFIG = {
    "wizard": os.environ.get("OTC_SETUP_WIZARD_URL", "http://127.0.0.1:80"),
    "setup_done_marker": "/var/lib/otc/setup-done",
    "install_complete_marker": "/etc/otc/.install-complete",
    "local_name": "Off The Cloud setup",
    # Milliseconds between two notifications: BlueZ has no back-pressure
    # for PropertiesChanged and a phone drops what it can't take.
    "notify_pace_ms": 6,
    "request_timeout_s": 60,
    "max_request_bytes": 65536,
}

UUID_BASE = "0f7c5e70-0b1e-4b8a-9c2d-5e7a1c0d{:04x}"
SERVICE_UUID = UUID_BASE.format(1)
REQUEST_UUID = UUID_BASE.format(2)
RESPONSE_UUID = UUID_BASE.format(3)
INFO_UUID = UUID_BASE.format(4)

FLAG_LAST = 0x01
PROTOCOL_VERSION = 1


def log(*args):
    print("[otc-setup-ble]", *args)


# ---------------------------------------------------------------------------
# The tunnel: framing + forwarding, independent of Bluetooth
# ---------------------------------------------------------------------------

class Tunnel:
    """Reassembles request chunks per stream id, forwards each finished
    request to the wizard on a worker thread, and hands the answer's
    chunks to `send(chunks)` (a list of bytes, in order)."""

    def __init__(self, wizard_url, send, chunk_size=20):
        self.wizard_url = wizard_url.rstrip("/")
        self.send = send
        self.chunk_size = chunk_size
        self.partial = {}
        self.lock = threading.Lock()

    MAX_CHUNK = 512  # an attribute value's ceiling (Bluetooth Core, ATT)

    def set_chunk_size(self, n):
        # 20 is what a 23-byte default MTU leaves; never go under it.
        self.chunk_size = min(self.MAX_CHUNK, max(20, int(n)))

    def handle_chunk(self, data):
        data = bytes(data)
        if len(data) < 2:
            return
        stream, flags, payload = data[0], data[1], data[2:]
        with self.lock:
            buf = self.partial.setdefault(stream, bytearray())
            buf += payload
            if len(buf) > CONFIG["max_request_bytes"]:
                del self.partial[stream]
                return
            if not flags & FLAG_LAST:
                return
            del self.partial[stream]
            message = bytes(buf)
        threading.Thread(target=self._serve, args=(stream, message), daemon=True).start()

    def _serve(self, stream, message):
        try:
            req = json.loads(message.decode("utf-8"))
            if isinstance(req.get("c"), int) and req["c"] < self.chunk_size:
                self.set_chunk_size(req["c"])
            status, ctype, body = self.forward(req)
        except Exception as e:  # noqa: BLE001
            status, ctype, body = 502, "application/json", json.dumps({"error": f"bad request: {e}"})
        self.send(self.frame(stream, self.encode_response(status, ctype, body)))

    def forward(self, req):
        method = str(req.get("m", "GET")).upper()
        path = str(req.get("p", "/"))
        if not path.startswith("/") or "://" in path:
            return 400, "application/json", json.dumps({"error": "bad path"})
        body = req.get("b") or ""
        data = body.encode("utf-8") if method == "POST" else None
        r = urllib.request.Request(self.wizard_url + path, data=data, method=method)
        if data is not None:
            r.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(r, timeout=CONFIG["request_timeout_s"]) as resp:
                return resp.status, resp.headers.get("Content-Type", "application/octet-stream"), resp.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            return e.code, e.headers.get("Content-Type", "application/json"), e.read().decode("utf-8", "replace")
        except Exception as e:  # noqa: BLE001
            return 503, "application/json", json.dumps({"error": f"the setup wizard is not answering: {e}"})

    @staticmethod
    def encode_response(status, ctype, body):
        raw = json.dumps({"s": int(status), "t": ctype, "b": body}, ensure_ascii=False).encode("utf-8")
        c = zlib.compressobj(6, zlib.DEFLATED, -15)  # raw deflate, no header
        return c.compress(raw) + c.flush()

    @staticmethod
    def decode_response(data):
        raw = zlib.decompress(bytes(data), -15)
        return json.loads(raw.decode("utf-8"))

    def frame(self, stream, message):
        size = max(1, self.chunk_size - 2)
        pieces = [message[i:i + size] for i in range(0, len(message), size)] or [b""]
        out = []
        for i, p in enumerate(pieces):
            flags = FLAG_LAST if i == len(pieces) - 1 else 0
            out.append(bytes([stream & 0xFF, flags]) + p)
        return out

    @staticmethod
    def frame_request(stream, req, chunk_size=20):
        """What a phone sends (the self-test's side of the wire)."""
        t = Tunnel("", None, chunk_size)
        return t.frame(stream, json.dumps(req).encode("utf-8"))


# ---------------------------------------------------------------------------
# Bluetooth (BlueZ over D-Bus)
# ---------------------------------------------------------------------------

def serve_bluetooth():
    import dbus
    import dbus.mainloop.glib
    import dbus.service
    from gi.repository import GLib

    BLUEZ = "org.bluez"
    OM_IFACE = "org.freedesktop.DBus.ObjectManager"
    PROP_IFACE = "org.freedesktop.DBus.Properties"
    GATT_MANAGER_IFACE = "org.bluez.GattManager1"
    AD_MANAGER_IFACE = "org.bluez.LEAdvertisingManager1"
    SERVICE_IFACE = "org.bluez.GattService1"
    CHRC_IFACE = "org.bluez.GattCharacteristic1"
    AD_IFACE = "org.bluez.LEAdvertisement1"

    class Application(dbus.service.Object):
        def __init__(self, bus):
            self.path = "/cloud/offthe/setup"
            self.services = []
            dbus.service.Object.__init__(self, bus, self.path)

        def get_path(self):
            return dbus.ObjectPath(self.path)

        @dbus.service.method(OM_IFACE, out_signature="a{oa{sa{sv}}}")
        def GetManagedObjects(self):
            out = {}
            for s in self.services:
                out[s.get_path()] = s.get_properties()
                for c in s.characteristics:
                    out[c.get_path()] = c.get_properties()
            return out

    class Service(dbus.service.Object):
        def __init__(self, bus, app, uuid):
            self.path = app.path + "/service0"
            self.uuid = uuid
            self.characteristics = []
            dbus.service.Object.__init__(self, bus, self.path)
            app.services.append(self)

        def get_path(self):
            return dbus.ObjectPath(self.path)

        def get_properties(self):
            return {SERVICE_IFACE: {
                "UUID": self.uuid, "Primary": True,
                "Characteristics": dbus.Array([c.get_path() for c in self.characteristics], signature="o"),
            }}

        @dbus.service.method(PROP_IFACE, in_signature="s", out_signature="a{sv}")
        def GetAll(self, interface):
            return self.get_properties()[SERVICE_IFACE]

    class Characteristic(dbus.service.Object):
        def __init__(self, bus, service, index, uuid, flags):
            self.path = service.path + "/char" + str(index)
            self.uuid = uuid
            self.flags = flags
            self.service = service
            self.notifying = False
            dbus.service.Object.__init__(self, bus, self.path)
            service.characteristics.append(self)

        def get_path(self):
            return dbus.ObjectPath(self.path)

        def get_properties(self):
            return {CHRC_IFACE: {
                "Service": self.service.get_path(), "UUID": self.uuid,
                "Flags": dbus.Array(self.flags, signature="s"),
                "Descriptors": dbus.Array([], signature="o"),
            }}

        @dbus.service.method(PROP_IFACE, in_signature="s", out_signature="a{sv}")
        def GetAll(self, interface):
            return self.get_properties()[CHRC_IFACE]

        @dbus.service.method(CHRC_IFACE, in_signature="a{sv}", out_signature="ay")
        def ReadValue(self, options):
            return dbus.Array(self.read(), signature="y")

        @dbus.service.method(CHRC_IFACE, in_signature="aya{sv}")
        def WriteValue(self, value, options):
            self.write(bytes(value), options)

        @dbus.service.method(CHRC_IFACE)
        def StartNotify(self):
            self.notifying = True

        @dbus.service.method(CHRC_IFACE)
        def StopNotify(self):
            self.notifying = False

        @dbus.service.signal(PROP_IFACE, signature="sa{sv}as")
        def PropertiesChanged(self, interface, changed, invalidated):
            pass

        def read(self):
            return b""

        def write(self, value, options):
            pass

        def notify(self, chunk):
            if self.notifying:
                self.PropertiesChanged(CHRC_IFACE, {"Value": dbus.Array(chunk, signature="y")}, [])

    class Advertisement(dbus.service.Object):
        def __init__(self, bus):
            self.path = "/cloud/offthe/setup/advertisement0"
            dbus.service.Object.__init__(self, bus, self.path)

        def get_path(self):
            return dbus.ObjectPath(self.path)

        def get_properties(self):
            return {AD_IFACE: {
                "Type": "peripheral",
                "ServiceUUIDs": dbus.Array([SERVICE_UUID], signature="s"),
                "LocalName": dbus.String(CONFIG["local_name"]),
                "Discoverable": dbus.Boolean(True),
            }}

        @dbus.service.method(PROP_IFACE, in_signature="s", out_signature="a{sv}")
        def GetAll(self, interface):
            return self.get_properties()[AD_IFACE]

        @dbus.service.method(AD_IFACE)
        def Release(self):
            # BlueZ dropped the advertisement (adapter reset, power
            # cycle): nobody can find the device any more, so start over.
            log("advertisement released - restarting")
            lost[0] = True
            mainloop.quit()

    dbus.mainloop.glib.DBusGMainLoop(set_as_default=True)
    bus = dbus.SystemBus()
    lost = [False]

    adapter = None
    objects = dbus.Interface(bus.get_object(BLUEZ, "/"), OM_IFACE).GetManagedObjects()
    for path, ifaces in objects.items():
        if GATT_MANAGER_IFACE in ifaces and AD_MANAGER_IFACE in ifaces:
            adapter = path
            break
    if adapter is None:
        log("no Bluetooth adapter with LE support - nothing to do")
        return 0
    props = dbus.Interface(bus.get_object(BLUEZ, adapter), PROP_IFACE)
    props.Set("org.bluez.Adapter1", "Powered", dbus.Boolean(True))

    app = Application(bus)
    service = Service(bus, app, SERVICE_UUID)
    mainloop = GLib.MainLoop()

    response_chrc = Characteristic(bus, service, 0, RESPONSE_UUID, ["notify"])

    # Notifications go out from the GLib thread, paced, one queue for all
    # streams so two answers interleave instead of one starving the other.
    queue = []
    queue_lock = threading.Lock()
    pumping = [False]

    def pump():
        with queue_lock:
            if not queue:
                pumping[0] = False
                return False
            chunk = queue.pop(0)
        response_chrc.notify(chunk)
        return True  # again after the pace

    def send(chunks):
        with queue_lock:
            queue.extend(chunks)
            if pumping[0]:
                return
            pumping[0] = True
        GLib.timeout_add(CONFIG["notify_pace_ms"], pump)

    tunnel = Tunnel(CONFIG["wizard"], send)

    class RequestChrc(Characteristic):
        def write(self, value, options):
            mtu = options.get("mtu")
            if mtu:
                tunnel.set_chunk_size(int(mtu) - 3)
            tunnel.handle_chunk(value)

    class InfoChrc(Characteristic):
        def read(self):
            return json.dumps({"v": PROTOCOL_VERSION, "name": socket.gethostname()}).encode()

    RequestChrc(bus, service, 1, REQUEST_UUID, ["write", "write-without-response"])
    InfoChrc(bus, service, 2, INFO_UUID, ["read"])

    def registered(what):
        def ok():
            log(what, "registered")
        return ok

    def failed(what):
        def err(e):
            log(what, "failed:", e)
            mainloop.quit()
        return err

    dbus.Interface(bus.get_object(BLUEZ, adapter), GATT_MANAGER_IFACE).RegisterApplication(
        app.get_path(), {}, reply_handler=registered("GATT service"), error_handler=failed("GATT service"))
    ad = Advertisement(bus)
    ad_manager = dbus.Interface(bus.get_object(BLUEZ, adapter), AD_MANAGER_IFACE)
    ad_manager.RegisterAdvertisement(
        ad.get_path(), {}, reply_handler=registered("advertisement"), error_handler=failed("advertisement"))

    def watch_done():
        # The same marker network_setup.py drops the hotspot on.
        if Path(CONFIG["setup_done_marker"]).exists() or Path(CONFIG["install_complete_marker"]).exists():
            log("setup done - stopping")
            try:
                ad_manager.UnregisterAdvertisement(ad.get_path())
            except Exception:  # noqa: BLE001
                pass
            mainloop.quit()
            return False
        return True

    GLib.timeout_add_seconds(5, watch_done)

    # bluetoothd restarting forgets our service and advertisement while
    # this process lives on, invisible: exit and let systemd start over
    # (Restart=on-failure), so a phone can find the device again.
    def bluez_owner_changed(name, old, new):
        if name == BLUEZ and old and old != new:
            log("bluetoothd restarted - restarting")
            lost[0] = True
            mainloop.quit()

    bus.add_signal_receiver(bluez_owner_changed, signal_name="NameOwnerChanged",
                            dbus_interface="org.freedesktop.DBus", arg0=BLUEZ)
    log("advertising", CONFIG["local_name"], "on", adapter, "- wizard at", CONFIG["wizard"])
    mainloop.run()
    return 1 if lost[0] else 0


# ---------------------------------------------------------------------------
# Self-test: the framing and the forwarding, no Bluetooth needed
# ---------------------------------------------------------------------------

def selftest():
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

    class Stub(BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def do_GET(self):
            body = ("<html>" + "x" * 3000 + "</html>").encode() if self.path == "/" else json.dumps({"path": self.path}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/html" if self.path == "/" else "application/json")
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            n = int(self.headers.get("Content-Length") or 0)
            echo = json.loads(self.rfile.read(n) or b"{}")
            status = 409 if echo.get("fail") else 200
            body = json.dumps({"echo": echo}).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(body)

    srv = ThreadingHTTPServer(("127.0.0.1", 0), Stub)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    port = srv.server_address[1]

    got = {}
    done = threading.Event()
    partial = {}

    def send(chunks):
        for c in chunks:
            stream, flags, payload = c[0], c[1], c[2:]
            partial.setdefault(stream, bytearray()).extend(payload)
            if flags & FLAG_LAST:
                got[stream] = Tunnel.decode_response(partial.pop(stream))
                if len(got) == 3:
                    done.set()

    tunnel = Tunnel(f"http://127.0.0.1:{port}", send, chunk_size=20)
    tunnel.set_chunk_size(514)  # what BlueZ's 517-byte MTU leaves...
    assert tunnel.chunk_size == 512  # ...but never more than an attribute holds
    tunnel.set_chunk_size(182)  # what an iPhone's 185-byte MTU leaves
    for c in Tunnel.frame_request(1, {"m": "GET", "p": "/"}, 20):
        tunnel.handle_chunk(c)
    for c in Tunnel.frame_request(7, {"m": "POST", "p": "/api/wifi", "b": json.dumps({"ssid": "home", "password": "pw"})}, 20):
        tunnel.handle_chunk(c)
    for c in Tunnel.frame_request(9, {"m": "POST", "p": "/api/x", "b": json.dumps({"fail": True})}, 20):
        tunnel.handle_chunk(c)
    assert done.wait(10), "answers did not all arrive"
    assert got[1]["s"] == 200 and got[1]["t"].startswith("text/html") and len(got[1]["b"]) == 3013, got[1]["s"]
    assert got[7]["s"] == 200 and got[7]["b"] == json.dumps({"echo": {"ssid": "home", "password": "pw"}}), got[7]
    assert got[9]["s"] == 409, got[9]
    # Chunk size respected, and the page compresses well enough to matter.
    chunks = tunnel.frame(1, Tunnel.encode_response(200, "text/html", "x" * 3000))
    assert all(len(c) <= 182 for c in chunks) and len(chunks) == 1, len(chunks)
    # A wizard that is not there is an answer too, not a hang.
    tunnel2 = Tunnel("http://127.0.0.1:1", send)
    assert tunnel2.forward({"m": "GET", "p": "/api/state"})[0] == 503
    assert tunnel2.forward({"m": "GET", "p": "http://evil/"})[0] == 400
    srv.shutdown()
    print("selftest ok")
    return 0


def main():
    if "--selftest" in sys.argv:
        return selftest()
    if Path(CONFIG["install_complete_marker"]).exists():
        log("this device is already set up - nothing to do")
        return 0
    os.system("rfkill unblock bluetooth 2>/dev/null")
    return serve_bluetooth()


if __name__ == "__main__":
    sys.exit(main())
