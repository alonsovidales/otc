# Demo device content

The sample library for the demo device (`docs/demo-device.md`, step 5): a made-up family's photos,
videos and files. Everything is invented. The people were generated with Gemini's image models,
and no real photo was used or sent anywhere. The output lives outside the repo, in
`~/src/otc-demo-content/`, because it is about 150 MB.

| Script | Does |
|---|---|
| `plan.py OUT` | Every photo's date, UTC offset, place (real coordinates), people, description and prompt. Writes `OUT/prompts/<id>.txt` and `plan.json`. `prompts/` here is a copy. |
| `generate.py OUT ids…` | Calls `docs/promo/tools/gen.py`, 5 at a time. The key comes only from the environment. Every call goes in `OUT/work/ledger.tsv` with its estimated cost, and no call starts past `--budget`. `--redo` makes a new version and `--model pro` overrides the model. |
| `finish.swift` | Turns a still into a camera JPEG: 2400 px long side, EXIF DateTimeOriginal/Digitized and offset, GPS, orientation 1, no make or model. |
| `docs.py OUT` | The 30 example files: PDFs printed by headless Chrome (muted, with its own profile), `.docx` made with `textutil`, `.xlsx` written as minimal SpreadsheetML, plus CSV, text, Markdown, the floor-plan PNG, the scanned receipt and Nora's drawing (SVG crayon). |
| `build.py OUT finish` | Fixes three stills, finishes all 110 photos into `library/Photos/…`, makes the 4 videos (`videos.py`) and writes the JSON files. |
| `videos.py` | Ken Burns moves and crossfades over 4-5 stills: 1920x1080 H.264, a silent AAC track, `creation_time` and an ISO 6709 `location`. |

Run them in this order:

```sh
OUT=~/src/otc-demo-content
python3 plan.py $OUT
GEMINI_KEY="$(security find-generic-password -s otc-gemini-api -w)" python3 generate.py $OUT p_lucia p_tom biscuit   # then pick refs
# copy the chosen portraits to $OUT/work/refs/<key>.jpg (lucia, tom, nora9, rosa, jan, leo5, nora7, leo3; biscuit.png),
# generate the portraits that depend on them, then every scene id
python3 docs.py $OUT
swiftc -O finish.swift -o /tmp/finish && python3 build.py $OUT /tmp/finish
```

The reference portraits come from `gemini-3-pro-image` and the scenes from
`gemini-2.5-flash-image`, with each person's portrait passed in. Children use the portrait
closest to their age: `nora7`/`leo3` before July 2025, then `nora9`/`leo5`. The faces were
checked with the device's own YuNet/SFace code (`face_recognition`, a small Go tool kept in
`$OUT/work/tools/facecheck`).

**Known issue: Nora and Leo merge.** SFace scores the generated brother and sister at 0.5-0.65,
well above the 0.363 same-person cutoff. Even their portraits score 0.66, partly because Leo was
generated with Nora as a reference "so they resemble each other". A simulation of
`matchOrNewPerson`, uploading in date order, gives one person with 88 faces for the two of them.
Lucía, Tom, Rosa and Jan each come out as a clean cluster. The fix is to generate Leo again
without Nora's portrait and with clearly different features, then regenerate his scenes (about
40 flash images, under $2). Another option is to accept it. Either way, real siblings will
merge on devices too.
