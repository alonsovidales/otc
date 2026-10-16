# Demo device content

The sample library for the demo device (`docs/demo-device.md`, step 5): a made-up family's photos,
videos and files. Everything is invented. The people were generated with Gemini's image models;
no real photo was used or sent anywhere. The output is kept outside the repo, in
`~/src/otc-demo-content/` (about 150 MB, plus `work/` with the raw generations): `library/` (114 photos, 4 videos, 30 files),
`manifest.json`, `people.json`, `collections.json`, `posts.json`, `profile.json` and `prompts/`.

| Script | Does |
|---|---|
| `plan.py OUT` | Every photo's date, UTC offset, place (real coordinates), people, description and prompt. Writes `OUT/prompts/<id>.txt` and `plan.json`; `prompts/` here is a copy. |
| `generate.py OUT ids…` | Calls `docs/promo/tools/gen.py`, 5 at a time. The key comes only from the environment. Every call goes in `OUT/work/ledger.tsv` with its estimated cost, and no call starts past `--budget`. `--redo` makes a new version; `--model pro` overrides the model. |
| `finish.swift` | Turns a still into a camera JPEG: 2400 px long side, EXIF DateTimeOriginal/Digitized and offset, GPS, orientation 1, no make or model. |
| `docs.py OUT` | The 30 example files: PDFs printed by headless Chrome (muted, its own profile), `.docx` via `textutil`, `.xlsx` as minimal SpreadsheetML, CSV, text and Markdown, the floor-plan PNG, the scanned receipt, and Nora's drawing (SVG crayon). |
| `build.py OUT finish [--only ids]` | Finishes the photos into `library/Photos/…` (`--only`: just those again), makes the 4 videos (`videos.py`) and writes the JSON files. A version that needed a local fix, a corrected people list or leaving out is named by its raw file, so a new version is never fixed blindly. |
| `videos.py` | Ken Burns moves and crossfades over 4-5 stills: 1920x1080 H.264, a silent AAC track, `creation_time` and an ISO 6709 `location`. |

Run them in this order:

```sh
OUT=~/src/otc-demo-content
python3 plan.py $OUT
GEMINI_KEY="$(security find-generic-password -s otc-gemini-api -w)" python3 generate.py $OUT p_lucia p_tom biscuit
# pick each reference into $OUT/work/refs/<key>.jpg (lucia, tom, nora9, rosa, jan, then leo5, nora7, leo3;
# biscuit.png) and $OUT/work/chosen.json, generating the portraits that depend on them; then every scene id
python3 docs.py $OUT
swiftc -O finish.swift -o /tmp/finish && python3 build.py $OUT /tmp/finish
```

The reference portraits came from `gemini-3-pro-image` and most scenes from
`gemini-2.5-flash-image`, with each person's portrait passed in. Children use the portrait closest
to their age: `nora7`/`leo3` before July 2025, then `nora9`/`leo5`. A few group photos (five or
six people) used `gemini-3-pro-image`. Faces were checked with the device's own YuNet/SFace code
(`face_recognition`, through a small Go tool kept in `$OUT/work/tools/facecheck`). A simulation of
`files_manager.matchOrNewPerson`, with its reference rule, groups each of the six people into one
clean person: Leo 45 faces, Nora 44, Tom 30, Lucía 28, Jan 12, Rosa 12.

**Siblings.** Children's faces are close together for SFace. Leo's first portraits were made with
Nora's as a reference and scored 0.57-0.66 against hers, so the two became one person. Leo was
made again from a description alone (straight dark hair with a fringe, a broad face, thick brows,
round glasses he always wears): 0.16-0.29 against Nora's portraits. In the scenes the two still
score 0.30 on average, and 18% of their pairs are above the 0.363 cutoff. A person only exists once
one of their faces is stored, so the first photos decide the split. If the first photo with
children shows both of them, the second child joins the first and they stay merged. The library's
first such photo (14 January 2024) shows Leo alone. With it they come out separate in date order,
in folder order, with the portraits first, and in 30 of 30 orders shuffled within each month.
Keep that photo first. A real family with two children would likely meet the same merge on a
device.

Other faces: Biscuit's face is detected in 10 close-ups and makes an unnamed person. About ten
blurred background faces (an audience, guests at a table) make one- to three-face people. Leave
them unnamed.
