#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Generates the demo library's images with docs/promo/tools/gen.py, a few at a time.
#   GEMINI_KEY=<your Gemini API key> \
#     python3 generate.py OUT [--jobs 4] [--budget 13.5] [--redo] [--model flash|pro] id...
# OUT is the content directory (OUT/prompts/plan.json from plan.py). Each call is
# OUT/work/raw/<id>-v<n>-1.<ext>; the version picked for the library is OUT/work/chosen.json
# (the newest unless set by hand). References are OUT/work/refs/<key>.<ext> - the portraits
# picked as each person's reference. Every call is logged with its estimated cost in
# OUT/work/ledger.tsv, and no call starts once the ledger reaches the budget.
# The key comes only from the environment and is never printed or written anywhere.
import concurrent.futures as cf, datetime, glob, json, os, subprocess, sys, threading

GEN = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "promo", "tools", "gen.py")
MODELS = {"flash": ("gemini-2.5-flash-image", 0.039), "pro": ("gemini-3-pro-image", 0.134)}

args = sys.argv[1:]
def opt(name, default):
    if name in args:
        i = args.index(name); v = args[i + 1]; del args[i:i + 2]; return v
    return default
jobs = int(opt("--jobs", "4")); budget = float(opt("--budget", "13.5")); force_model = opt("--model", None)
redo = "--redo" in args
if redo: args.remove("--redo")
root, ids = args[0], args[1:]
work = os.path.join(root, "work")
raw, refdir = os.path.join(work, "raw"), os.path.join(work, "refs")
os.makedirs(raw, exist_ok=True)
ledger = os.path.join(work, "ledger.tsv")
items = {o["id"]: o for o in json.load(open(os.path.join(root, "prompts", "plan.json")))["items"]}
lock = threading.Lock()

def spent():
    if not os.path.exists(ledger): return 0.0
    return sum(float(l.split("\t")[5]) for l in open(ledger) if l.strip())

def versions(id):
    return sorted(int(p.rsplit("-v", 1)[1].split("-")[0]) for p in glob.glob(os.path.join(raw, f"{id}-v*-1.*")))

def ref_file(key):
    m = glob.glob(os.path.join(refdir, key + ".*"))
    if not m: raise SystemExit(f"missing reference {key} in {refdir}")
    return m[0]

def run(id):
    o = items[id]
    if versions(id) and not redo:
        return f"{id}: have v{versions(id)[-1]}"
    kind = force_model or o["model"]
    model, cost = MODELS[kind]
    size = "2K" if kind == "pro" else "-"
    with lock:
        if spent() + cost > budget:
            return f"{id}: SKIPPED, budget reached ({spent():.2f})"
        v = (versions(id) or [0])[-1] + 1
        while os.path.exists(os.path.join(raw, f"{id}-v{v}.pending")): v += 1
        open(os.path.join(raw, f"{id}-v{v}.pending"), "w").close()
    prefix = os.path.join(raw, f"{id}-v{v}")
    pfile = os.path.join(root, "prompts", id + ".txt")
    refs = [ref_file(k) for k in o["refs"]]
    r = subprocess.run([sys.executable, GEN, model, prefix, o["aspect"], size, pfile] + refs,
                       capture_output=True, text=True, env=os.environ)
    os.remove(prefix + ".pending")
    out = (r.stdout + r.stderr).strip()
    ok = bool(glob.glob(prefix + "-1.*"))
    # An HTTP error isn't billed; any answer is counted as a full image to stay on the safe side.
    billed = cost if (ok or "usage:" in out) else 0.0
    with lock:
        with open(ledger, "a") as f:
            f.write("\t".join([datetime.datetime.now().isoformat(timespec="seconds"), id, f"v{v}", model,
                               "ok" if ok else "fail", f"{billed:.3f}"]) + "\n")
    if not ok:
        open(prefix + ".log", "w").write(out[-3000:])
    note = " | ".join(l for l in out.splitlines() if l.startswith(("text:", "HTTP")))[:300]
    return f"{id}: v{v} {'ok' if ok else 'FAILED'} {note}"

with cf.ThreadPoolExecutor(jobs) as ex:
    for line in ex.map(run, ids):
        print(line, flush=True)
print(f"spent so far: ${spent():.2f}")
