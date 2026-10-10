#!/usr/bin/env python3
"""Fixture: the setup wizard. "A docstring is not counted." """

# BEGIN GENERATED I18N
I18N = {"en": {"wiz.title": "Set up your device"}}
# END GENERATED I18N


def handle(self, data, ssid):
    if not ssid:
        self.send_json(400, {"error": "choose a network"})
    self.send_json(400, {"error": f"{ssid} needs a password"})
    self.send_json(400, {"error": "a network with a very long name "
                                  "across two lines"})
    self.send_json(400, {"error": "not_found", "code": "not_found"})
    raise RuntimeError(data.get("error", "could not reserve that name"))
    msg = data.get("error") or "could not check that setup code"
    print("Starting the wizard on port 80")  # logs are not counted
    self.send_json(409, {"error": "this device is already set up"})  # i18n-ignore: fixture for the escape hatch


PAGE = r"""<!doctype html>
<html lang="en"><head><title>Off The Cloud setup</title></head>
<body>
<h1>Let's set up your device</h1>
<p>This takes <b>about ten minutes</b>.</p>
<label>Device password</label>
<script>
  view.innerHTML = `<h2>1 · Connect to your WiFi</h2><button>Join</button>`;
  status.textContent = 'Connecting…';
  msg(r.error || 'Failed');
</script>
</body></html>
"""
