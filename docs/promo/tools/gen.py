#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# docs/promo: one image from Gemini's image models ("Nano Banana"), saved as out_prefix-1.png.
#   GEMINI_KEY=... gen.py model out_prefix aspect size prompt_file [reference images...]
# size: 1K, 2K or 4K for gemini-3-pro-image; "-" for models that take none (gemini-2.5-flash-image).
import base64, json, os, sys, urllib.request, mimetypes
model, prefix, aspect, size, pfile = sys.argv[1:6]
refs = sys.argv[6:]
parts = []
for r in refs:
    mt = mimetypes.guess_type(r)[0] or "image/jpeg"
    parts.append({"inline_data": {"mime_type": mt, "data": base64.b64encode(open(r, "rb").read()).decode()}})
parts.append({"text": open(pfile).read()})
body = {"contents": [{"parts": parts}],
        "generationConfig": {"responseModalities": ["IMAGE", "TEXT"], "imageConfig": ({"aspectRatio": aspect} if size == "-" else {"aspectRatio": aspect, "imageSize": size})}}
req = urllib.request.Request(f"https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent",
    data=json.dumps(body).encode(), headers={"Content-Type": "application/json", "x-goog-api-key": os.environ["GEMINI_KEY"]})
try:
    resp = json.load(urllib.request.urlopen(req, timeout=300))
except urllib.error.HTTPError as e:
    print("HTTP", e.code, e.read().decode()[:600]); sys.exit(1)
n = 0
for c in resp.get("candidates", []):
    for p in c.get("content", {}).get("parts", []):
        d = p.get("inlineData") or p.get("inline_data")
        if d:
            n += 1
            ext = ".png" if "png" in d.get("mimeType", d.get("mime_type", "")) else ".jpg"
            open(f"{prefix}-{n}{ext}", "wb").write(base64.b64decode(d["data"]))
            print("saved", f"{prefix}-{n}{ext}")
        elif p.get("text"):
            print("text:", p["text"][:300])
u = resp.get("usageMetadata", {})
print("usage:", {k: u.get(k) for k in ("promptTokenCount", "candidatesTokenCount", "totalTokenCount")})
if not n: print(json.dumps(resp)[:800])
