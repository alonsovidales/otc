# Promo images

Marketing images for the website and the app stores: the device - the Raspberry Pi in its purple
case with its two USB card readers - beside a screen showing the web app, or a phone showing the
Android app. The scenes are AI-generated illustrations, not photos.

| File | Scene |
|---|---|
| `pi-desk-monitor.jpg` | bright home office, the Pi beside a monitor |
| `pi-photographer-desk.jpg` | camera, lenses and prints on the wall, the Pi beside a monitor |
| `pi-family-living-room.jpg` | evening living room, the Pi beside a laptop, family photos around |
| `pi-closeup.jpg` | close-up of the Pi, the monitor out of focus behind it |
| `pi-android-phone.jpg` | sunlit living room, the Pi on a round side table beside an Android phone on a stand showing the Android app's Images |
| `web-app-2880x1620.png`, `web-app-2880x1800.png` | the screen content alone: the web app in a browser window on a desktop, at 2x (16:9, and 16:10 - a Mac App Store size) |
| `android-app-1080x2322.png` | the phone's screen content alone: the Android app's Images, light theme, status and gesture bars included |

`pi-android-phone.jpg` is also ready for Google Play as it is (a JPEG, 2752 x 1536: each side
between 320 and 3840 px, the long side under twice the short one).

## How they are made

1. **The scenes** (`source/scene-*.jpg`, 2752 × 1536) come from Gemini's Nano Banana Pro
   (`gemini-3-pro-image`, 16:9, 2K) with `bridge/static/img/device.jpg` as the
   reference for the device and the prompts in `source/prompts/scene-*.txt`, which ask for a blank
   pure-white screen with all four corners in view.
2. **The screen content** is the real web app, rendered offline: `tools/serve.mjs` serves `web/`
   with `net/useWS.ts` swapped for `tools/mock-device.ts`, a pretend device whose library is the
   20 sample photos in `source/library/` (generated with `gemini-2.5-flash-image` from
   `source/prompts/library.txt`; the people in them don't exist). `tools/desk.html`
   puts the app in a browser window on a desktop, with a margin all round so no screen corner
   touches the window's buttons, and `tools/shot.mjs` captures it at 2x with a headless Chrome.
   The Android capture is the real app (1.1), a debug build with its own application id
   (`applicationIdSuffix ".promo"`, and its FileProvider authority changed to match) on a
   Pixel 8 emulator, signed in through Welcome's "Custom address" to a stand-in device that
   answers with the same 20 sample photos, dated as `mock-device.ts` dates them, and no people,
   tags or collections. The emulator's screen was set to the phone's aspect
   (`adb shell wm size 1080x2322`), its status
   bar cleaned with System UI demo mode (3:30, full Wi-Fi and battery, no mobile signal or
   notifications), and the screen taken with `adb exec-out screencap -p` (24-bit, no alpha).
3. **`tools/composite.swift`** finds the white screen in a scene (flood fill, a straight line fitted
   to each edge), warps the capture onto it supersampled, and blends it in by brightness, so the
   bezel and the screen's soft edge stay the photo's own. Anything colourful in front of the screen
   (the case's lid in the close-up) is never covered. `--hole x y r` puts a phone's camera hole
   back over the page (the flood fill takes it in with the screen).

```sh
npm install --prefix web                     # once: serve.mjs uses web/'s Vite
node docs/promo/tools/serve.mjs &            # http://127.0.0.1:5288/
node docs/promo/tools/shot.mjs "http://127.0.0.1:5288/promo/tools/desk.html?mt=26&mb=22" /tmp/desk-16x9.png 1440 810 7000
node docs/promo/tools/shot.mjs "http://127.0.0.1:5288/promo/tools/desk.html?mt=28&mb=26" /tmp/desk-16x10.png 1440 900 7000
swiftc -O docs/promo/tools/composite.swift -o /tmp/composite
cd docs/promo/source
/tmp/composite scene-desk.jpg         /tmp/desk-16x9.png  /tmp/pi-desk-monitor.png        1880 600 --aspect 1.7778
/tmp/composite scene-photographer.jpg /tmp/desk-16x9.png  /tmp/pi-photographer-desk.png   1844 550 --aspect 1.7778
/tmp/composite scene-family.jpg       /tmp/desk-16x10.png /tmp/pi-family-living-room.png  1211 619 --aspect 1.6 --lift 0.02
/tmp/composite scene-closeup.jpg      /tmp/desk-16x9.png  /tmp/pi-closeup.png             1300 450 --aspect 1.7778 --toph 0.5625 --blur 7 --reach 14 --expand 30
/tmp/composite scene-android.jpg      ../android-app-1080x2322.png ../pi-android-phone.jpg 1835 718 --aspect 0.4651 --hole 1962 234 15
```

The two numbers after the output are a point on the white screen. `desk.html` takes `host=` for
the address shown, `wall=` for the library photo used (blurred) as the wallpaper, and `m=`, `mt=`,
`mb=` for the window's margins; `shot.mjs` takes `light` as a last argument for the light theme.
A new scene: `GEMINI_KEY=... python3 docs/promo/tools/gen.py gemini-3-pro-image /tmp/scene 16:9 2K
docs/promo/source/prompts/scene-desk.txt bridge/static/img/device.jpg`.

Devices never download this folder (`.gitattributes`, `export-ignore`).
