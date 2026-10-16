#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Puts the demo library together from the generated stills (generate.py), the 20 docs/promo
# samples and the example files (docs.py):
#   swiftc -O finish.swift -o /tmp/finish
#   python3 build.py OUT /tmp/finish [--only id,id,...]   (--only: finish just those photos again)
# - fixes the few stills that need it (text on a doormat and on life vests, a book's title);
# - finishes every photo as a camera JPEG (finish.swift: 2400 px long side, EXIF date, offset,
#   GPS, orientation 1, no make/model) under library/Photos/..., file times = the photo's date;
# - makes the videos (videos.py) and writes manifest.json, people.json, collections.json,
#   posts.json and profile.json (+ profile/avatar.jpg).
import datetime as dt, glob, json, os, random, shutil, subprocess, sys

OUT, FINISH = sys.argv[1], sys.argv[2]
HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", "..", ".."))
WORK = os.path.join(OUT, "work")
RAW = os.path.join(WORK, "raw")
plan = json.load(open(os.path.join(OUT, "prompts", "plan.json")))
items = plan["items"]
ledger = [l.rstrip("\n").split("\t") for l in open(os.path.join(WORK, "ledger.tsv")) if l.strip()]
chosen = json.load(open(os.path.join(WORK, "chosen.json"))) if os.path.exists(os.path.join(WORK, "chosen.json")) else {}

args = sys.argv[3:]
ONLY = set(args[args.index("--only") + 1].split(",")) if "--only" in args else None

# Everything below is tied to one generated version (the raw file's name): a new version of the
# shot needs looking at again.
# Not used: a version that came out wrong and has no newer one.
DROPPED = {"s33": ("s33-v1-1.png", "Nora looked much younger than 8 and her face matched Leo's reference better than her own")}
# Who is really in the picture when the model left someone out.
PEOPLE_FIX = {"s28": ("s28-v1-1.png", ["Lucía", "Nora", "Leo", "Jan"]), "s93": ("s93-v1-1.png", ["Tom", "Nora", "Leo"]),
              "s88": ("s88-v1-1.png", ["Rosa", "Jan"]), "r16": ("r16-v1-1.png", ["Nora"])}
DESC_FIX = {"s28": ("s28-v1-1.png", "Christmas Eve dinner at Rosa and Jan's (Rosa behind the camera)"),
            "s93": ("s93-v1-1.png", "Throwing leaves in Amelisweerd (Lucía behind the camera)"),
            "r16": ("r16-v1-1.png", "Nora's ice cream in the old town")}
# Local fixes of text the model drew (ffmpeg filter graphs on the raw still).
FIXES = {
    # "HOME" on the doormat behind the children's feet: the mat's own rows from just above,
    # copied over the letters in the gaps between their legs.
    "s58": ("s58-v1-1.png", "[0]split=5[m][s1][s2][s3][s4];[s1]crop=25:30:277:931[p1];[s2]crop=121:30:346:931[p2];"
           "[s3]crop=18:30:515:931[p3];[s4]crop=60:30:581:931[p4];[m][p1]overlay=277:960[o1];"
           "[o1][p2]overlay=346:960[o2];[o2][p3]overlay=515:960[o3];[o3][p4]overlay=581:960"),
    # A made-up brand name printed on both life vests.
    "s51": ("s51-v1-1.png", "delogo=x=322:y=721:w=97:h=38"),
    # The open picture book's title: the book, close to the lens, out of focus like a real one.
    "s63": ("s63-v1-1.png", "[0]format=rgba,split[a][b];[b]gblur=sigma=6[bl];color=black:s=1184x864:d=1,format=gray,"
           "geq=lum='255*clip((1-((X-370)*(X-370)/(300*300)+(Y-610)*(Y-610)/(190*190)))*2.5,0,1)'[m];"
           "[bl][m]alphamerge[blm];[a][blm]overlay=format=auto,format=rgb24"),
}
# Nora's vest's letters are dark on orange: each dark pixel (and its neighbours) takes the colour
# 40 rows below, which is plain vest.
def _dark_to(box, dy, t):
    x0, x1, y0, y1 = box
    lum = lambda x, y: f"(0.3*r({x},{y})+0.59*g({x},{y})+0.11*b({x},{y}))"
    near = "+".join(f"lt({lum(f'X+{a}', f'Y+{b}')},{t})" for a in (-2, 0, 2) for b in (-2, 0, 2))
    return "format=rgb24,geq=" + ":".join(
        f"{c}='if(between(X,{x0},{x1})*between(Y,{y0},{y1})*gt({near},0),{c}(X,Y+{dy}),{c}(X,Y))'" for c in "rgb")
FIXES["s51"] = (FIXES["s51"][0], FIXES["s51"][1] + "," + _dark_to((1022, 1112, 716, 760), 40, 115))

def for_version(table, id, raw):
    """The entry of a version-tied table for this raw still, or None."""
    e = table.get(id)
    return e[1] if e and raw and os.path.basename(raw) == e[0] else None

def raw_of(id):
    if id in chosen:
        return os.path.join(RAW, chosen[id])
    vs = sorted(glob.glob(os.path.join(RAW, f"{id}-v*-1.*")), key=lambda p: int(p.rsplit("-v", 1)[1].split("-")[0]))
    return vs[-1] if vs else None

def model_of(path):
    v = os.path.basename(path).rsplit("-v", 1)[1].split("-")[0]
    id = os.path.basename(path).rsplit("-v", 1)[0]
    for l in ledger:
        if l[1] == id and l[2] == "v" + v:
            return l[3]
    return "?"

def local(when, off):
    d = dt.datetime.strptime(when, "%Y-%m-%d %H:%M:%S")
    h, m = int(off[1:3]), int(off[4:6])
    sign = 1 if off[0] == "+" else -1
    return d.replace(tzinfo=dt.timezone(sign * dt.timedelta(hours=h, minutes=m)))

def jitter(id, gps, place):
    if not gps:
        return None
    r = random.Random(id)
    j = 0.0003 if place in ("home", "grandparents") else 0.0006
    return [round(gps[0] + r.uniform(-j, j), 6), round(gps[1] + r.uniform(-j, j), 6)]

EVENT = {"Rome": "Rome", "Dolomites": "Dolomites", "Algarve": "Algarve", "Zillertal": "Zillertal"}

def event_of(o):
    d = o["when"][:7]
    if "2024-04-19" <= o["when"][:10] <= "2024-04-21": return "Rome weekend, April 2024"
    if "2024-07-20" <= o["when"][:10] <= "2024-07-27": return "Camping in the Dolomites, July 2024"
    if "2025-08-09" <= o["when"][:10] <= "2025-08-20": return "Algarve, August 2025"
    if "2026-02-14" <= o["when"][:10] <= "2026-02-21": return "Ski week in the Zillertal, February 2026"
    if o["when"][:10] == "2026-03-14": return "Leo's 5th birthday"
    if "2024-12-24" <= o["when"][:10] <= "2024-12-26": return "Christmas 2024 at Rosa and Jan's"
    if "2025-12-20" <= o["when"][:10] <= "2025-12-26": return "Christmas 2025 at Rosa and Jan's"
    if o["when"][:10] == "2025-06-19": return "Nora's school play"
    if o["when"][:10] == "2025-09-01": return "First day of school"
    if d == "2026-10" or o["when"][:10] in ("2024-10-19", "2024-10-26"): return "Autumn walks"
    return None

# ------------------------------------------------------------------ photos
photos = [o for o in items if o["kind"] in ("portrait", "scene", "reused")]
photos.sort(key=lambda o: o["when"])
fixed_dir = os.path.join(WORK, "fixed")
os.makedirs(fixed_dir, exist_ok=True)
jobs, manifest, skipped = [], [], []
dsc = 212
last_camera = None
rnd = random.Random(7)
used = set()
for o in photos:
    id = o["id"]
    if o["kind"] != "reused" and for_version(DROPPED, id, raw_of(id)):
        skipped.append(dict(id=id, why=for_version(DROPPED, id, raw_of(id)))); continue
    if o["kind"] == "reused":
        src = os.path.join(REPO, o["source"])
        made = "docs/promo sample " + os.path.basename(o["source"])
    else:
        src = raw_of(id)
        if not src:
            skipped.append(dict(id=id, why="not generated (the Gemini API's prepaid credit ran out)")); continue
        made = model_of(src)
        graph = for_version(FIXES, id, src)
        if graph and (ONLY is None or id in ONLY):
            fixed = os.path.join(fixed_dir, id + ".png")
            flag = "-filter_complex" if "[" in graph else "-vf"
            subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", src, flag, graph, "-frames:v", "1", fixed], check=True)
        if graph:
            src = os.path.join(fixed_dir, id + ".png")
    t = local(o["when"], o["off"])
    yyyy, mm = t.strftime("%Y"), t.strftime("%m")
    if o["src"].startswith("camera:"):
        # A few shots apart within a trip, a few hundred between trips (the ones not kept).
        dsc += rnd.randint(1, 23) if o["src"] == last_camera else rnd.randint(180, 640)
        last_camera = o["src"]
        rel = f"library/Photos/Tom's camera/{yyyy}/{EVENT[o['src'][7:]]}/DSC_{dsc:04d}.jpg"
    else:
        name = f"IMG_{t.strftime('%Y%m%d_%H%M%S')}.jpg"
        rel = f"library/Photos/Lucía's phone/{yyyy}/{mm}/{name}"
    assert rel not in used, rel
    used.add(rel)
    gps = jitter(id, o.get("gps"), o.get("place"))
    if ONLY is None or id in ONLY:
        jobs.append("\t".join([src, os.path.join(OUT, rel), "2400", t.strftime("%Y:%m:%d %H:%M:%S"), o["off"]] +
                              ([f"{gps[0]:.6f}", f"{gps[1]:.6f}"] if gps else [])))
    people = for_version(PEOPLE_FIX, id, src if o["kind"] == "reused" else raw_of(id)) or o["people"]
    entry = dict(path=rel, type="photo", date=t.isoformat(), gps=dict(lat=gps[0], lon=gps[1]) if gps else None,
                 people=[plan["people"][p] for p in people],
                 description=for_version(DESC_FIX, id, raw_of(id)) or o["desc"],
                 event=event_of(o), id=id, made_with=made)
    if o["kind"] == "reused" and o.get("unnamed_faces"):
        entry["unnamed_faces"] = o["unnamed_faces"]
    if o["kind"] != "reused":
        entry["prompt"] = f"prompts/{id}.txt"
        if graph:
            entry["retouched"] = True
    if o["kind"] == "portrait":
        entry["reference_portrait"] = o["ref_key"]
    manifest.append(entry)

def nfc_tree(root):
    """Foundation writes new folders' names decomposed (NFD); a phone writes them composed (NFC),
    and so does the manifest. Rename through a temporary name: APFS doesn't tell the two apart."""
    import unicodedata
    for dirpath, dirs, files in os.walk(root, topdown=False):
        for n in dirs + files:
            c = unicodedata.normalize("NFC", n)
            if c != n:
                tmp = os.path.join(dirpath, ".nfc-" + str(os.getpid()))
                os.rename(os.path.join(dirpath, n), tmp)
                os.rename(tmp, os.path.join(dirpath, c))

for j in jobs:  # the folders exist (composed names) before finish.swift writes into them
    os.makedirs(os.path.dirname(j.split("\t")[1]), exist_ok=True)
jobfile = os.path.join(WORK, "finish-jobs.tsv")
open(jobfile, "w").write("\n".join(jobs) + "\n")
r = subprocess.run([FINISH, jobfile], capture_output=True, text=True) if jobs else None
if r and r.returncode:
    print(r.stdout[-2000:], r.stderr[-2000:]); sys.exit(1)
nfc_tree(os.path.join(OUT, "library"))
for e in manifest:
    ts = dt.datetime.fromisoformat(e["date"]).timestamp()
    os.utime(os.path.join(OUT, e["path"]), (ts, ts))
print(len(manifest), "photos (" + (f"{len(jobs)} finished" if ONLY is not None else "all finished") + ");",
      "left out:", ", ".join(s["id"] for s in skipped) or "none")
by_id = {e["id"]: e for e in manifest}

# ------------------------------------------------------------------ videos
VIDEOS = [
    dict(id="v_beach", stills=["s48", "s49", "s57", "r11"], when="2025-08-11 13:05:42", off="+01:00", place="meia_praia",
         desc="Beach day in the Algarve: digging, jumping waves and a sunset swing", people=["Lucía", "Tom", "Nora", "Leo"],
         event="Algarve, August 2025"),
    dict(id="v_ski", stills=["s74", "s71", "s72", "s76", "s77"], when="2026-02-18 15:48:09", off="+01:00", place="penken",
         desc="Ski week: Nora on the slopes, Leo's ski school and lunch at the hut", people=["Lucía", "Tom", "Nora", "Leo"],
         event="Ski week in the Zillertal, February 2026"),
    dict(id="v_birthday", stills=["s80", "s81", "s79", "s82"], when="2026-03-14 17:20:31", off="+01:00", place="home",
         desc="Leo's 5th birthday: presents, balloons, five candles and Grandma and Grandpa", people=["Lucía", "Nora", "Leo", "Rosa", "Jan"],
         event="Leo's 5th birthday"),
    dict(id="v_christmas", stills=["s65", "s66", "s67", "s68"], when="2025-12-25 16:02:57", off="+01:00", place="grandparents",
         desc="Christmas 2025 at Rosa and Jan's: the cookies, the lasagna, the presents and the family photo",
         people=["Lucía", "Tom", "Nora", "Leo", "Rosa", "Jan"], event="Christmas 2025 at Rosa and Jan's"),
]
vids = []
for v in VIDEOS:
    t = local(v["when"], v["off"])
    rel = f"library/Photos/Lucía's phone/{t.strftime('%Y')}/{t.strftime('%m')}/VID_{t.strftime('%Y%m%d_%H%M%S')}.mp4"
    gps = jitter(v["id"], plan["places"][v["place"]], v["place"])
    stills = [os.path.join(OUT, by_id[s]["path"]) for s in v["stills"]]
    subprocess.run([sys.executable, os.path.join(HERE, "videos.py"), os.path.join(OUT, rel),
                    t.astimezone(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000000Z"),
                    f"{gps[0]:+08.4f}{gps[1]:+09.4f}/"] + stills, check=True)
    ts = t.timestamp()
    os.utime(os.path.join(OUT, rel), (ts, ts))
    dur = float(subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0",
                                os.path.join(OUT, rel)], capture_output=True, text=True).stdout.strip())
    vids.append(dict(path=rel, type="video", date=t.isoformat(), gps=dict(lat=gps[0], lon=gps[1]),
                     people=[plan["people"][p] for p in v["people"]], description=v["desc"], event=v["event"],
                     id=v["id"], duration_s=round(dur, 1), made_from=[by_id[s]["path"] for s in v["stills"]]))
    print("  ", rel, round(dur, 1), "s")

# Photos and videos from an earlier build that aren't in this one (a shot moved to another date).
import unicodedata
keep = {unicodedata.normalize("NFC", e["path"]) for e in manifest + vids}
for p in glob.glob(os.path.join(OUT, "library", "Photos", "**", "*.*"), recursive=True):
    rel = unicodedata.normalize("NFC", os.path.relpath(p, OUT))
    if rel not in keep and rel.lower().endswith((".jpg", ".mp4")):
        os.remove(p); print("   removed stale", rel)

# ------------------------------------------------------------------ files
docs = json.load(open(os.path.join(WORK, "docs.json")))
files = []
for d in docs:
    ext = d["path"].rsplit(".", 1)[1].lower()
    files.append(dict(path="library/" + d["path"], type={"pdf": "pdf", "docx": "word", "xlsx": "spreadsheet", "csv": "spreadsheet",
                                                       "txt": "text", "md": "markdown", "png": "image", "jpg": "image"}[ext],
                      date=d["date"], gps=None, people=[], description=d["description"]))

everything = sorted(manifest + vids, key=lambda e: e["date"]) + sorted(files, key=lambda e: e["path"])
json.dump(dict(about="The Off The Cloud demo device's sample content. Everything is made up: the people were generated "
                     "with Gemini's image models (prompts in prompts/), the documents were written for the demo.",
               library_root="library", count=dict(photos=len(manifest), videos=len(vids), files=len(files)),
               left_out=skipped, items=everything),
          open(os.path.join(OUT, "manifest.json"), "w"), ensure_ascii=False, indent=1)

# ------------------------------------------------------------------ people
refs = {e["reference_portrait"]: e["path"] for e in manifest if "reference_portrait" in e}
count = lambda n: sum(1 for e in manifest if n in e["people"])
people = [dict(name=plan["people"][k], role=role, reference_portraits=[refs[r] for r in rs], photos=count(plan["people"][k]))
          for k, role, rs in [("Lucía", "mother, born 1988", ["lucia"]), ("Tom", "father, born 1986", ["tom"]),
                              ("Nora", "daughter, born December 2016", ["nora7", "nora9"]),
                              ("Leo", "son, born 14 March 2021", ["leo3", "leo5"]),
                              ("Rosa", "Tom's mother, born 1958", ["rosa"]), ("Jan", "Tom's father, born 1956", ["jan"])]]
json.dump(dict(people=people,
               not_named=["Biscuit, the golden retriever, isn't a person; the face detector still picks up his face in "
                          "the close-ups, which makes an unnamed 'person' of him - leave it unnamed (or delete it).",
                          "A few blurred background faces (an audience, a dinner table) may make one-face people: leave "
                          "them unnamed."]),
          open(os.path.join(OUT, "people.json"), "w"), ensure_ascii=False, indent=1)

# ------------------------------------------------------------------ collections
def paths(pred): return [e["path"] for e in sorted(manifest + vids, key=lambda e: e["date"]) if pred(e)]
dog = {"biscuit", "s02", "s14", "s25", "s36", "s62", "s63", "s69", "s86", "s93", "r02"}
collections = [
    dict(name="Algarve 2025", paths=paths(lambda e: e.get("event") == "Algarve, August 2025")),
    dict(name="Christmas at Rosa & Jan's", paths=paths(lambda e: (e.get("event") or "").startswith("Christmas"))),
    dict(name="Leo turns 5", paths=paths(lambda e: e.get("event") == "Leo's 5th birthday")),
    dict(name="Mountains", paths=paths(lambda e: e.get("event") in ("Camping in the Dolomites, July 2024",
                                                                    "Ski week in the Zillertal, February 2026"))),
    dict(name="Biscuit", paths=paths(lambda e: e.get("id") in dog)),
]
json.dump(collections, open(os.path.join(OUT, "collections.json"), "w"), ensure_ascii=False, indent=1)

# ------------------------------------------------------------------ posts
P = lambda *ids: [by_id[i]["path"] for i in ids]
posts = [
    dict(caption="Two weeks of sun, sea and caves in the Algarve. Leo has decided he lives on the beach now.",
         date="2025-08-21T20:30:00+02:00", photos=P("s55", "s50", "s48", "s47")),
    dict(caption="Christmas at Rosa and Jan's: lasagna, cookies and the whole family by the tree.",
         date="2025-12-27T11:15:00+01:00", photos=P("s68", "s66", "s65")),
    dict(caption="First ski week! Nora on the red runs, Leo the star of ski school.",
         date="2026-02-22T19:40:00+01:00", photos=P("s71", "s72", "s74")),
    dict(caption="Leo is five! Dinosaur cake, five candles and one very happy boy.",
         date="2026-03-15T10:05:00+01:00", photos=P("s79", "s80", "s84")),
]
json.dump(posts, open(os.path.join(OUT, "posts.json"), "w"), ensure_ascii=False, indent=1)

# ------------------------------------------------------------------ profile
os.makedirs(os.path.join(OUT, "profile"), exist_ok=True)
subprocess.run(["ffmpeg", "-v", "error", "-y", "-i", raw_of("biscuit"), "-vf", "crop=600:600:310:20,scale=512:512:flags=lanczos",
                "-q:v", "3", os.path.join(OUT, "profile", "avatar.jpg")], check=True)
json.dump(dict(display_name="The Demo Family",
               description="This is the Off The Cloud demo device: the family, their photos and their files are all made up.",
               avatar="profile/avatar.jpg"),
          open(os.path.join(OUT, "profile.json"), "w"), ensure_ascii=False, indent=1)
print("manifest:", len(manifest), "photos,", len(vids), "videos,", len(files), "files")
