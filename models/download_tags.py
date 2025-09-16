# download_tags_fixed.py
from pathlib import Path
import json
from huggingface_hub import snapshot_download

OUT_DIR = Path("models/ram_plus/tags")
OUT_DIR.mkdir(parents=True, exist_ok=True)

ds_dir = snapshot_download(
    repo_id="xinyu1205/recognize-anything-plus-model-tag-descriptions",
    repo_type="dataset",
    allow_patterns=["ram_tag_list_4585_llm_tag_descriptions.json"],
    local_dir=OUT_DIR.as_posix(),
    token=None,  # force anonymous; avoids stale/bad token issues
)

src = Path(ds_dir) / "ram_tag_list_4585_llm_tag_descriptions.json"
dst = OUT_DIR / "tag_list_4585.txt"

raw_text = src.read_text(encoding="utf-8")
raw = json.loads(raw_text)

tags, seen = [], set()

def add(tag: str):
    t = tag.strip().lower()
    if t and t not in seen:
        seen.add(t)
        tags.append(t)

if isinstance(raw, list):
    # Most common: list of single-key dicts -> take the key
    for item in raw:
        if isinstance(item, dict) and item:
            k = next(iter(item.keys()))
            add(k)
        elif isinstance(item, str):
            add(item)
        elif isinstance(item, dict):
            # Multi-key dict variant: try known fields
            for key in ("name", "tag", "english", "label"):
                v = item.get(key)
                if isinstance(v, str):
                    add(v); break
elif isinstance(raw, dict):
    # Dict-of-descriptions variant -> keys are tags
    for k in raw.keys():
        add(k)
else:
    raise SystemExit(f"Unexpected JSON root type: {type(raw)}")

dst.write_text("\n".join(tags), encoding="utf-8")
print(f"Read from: {src.resolve()}")
print(f"Wrote {len(tags)} tags → {dst.resolve()}")
print("Preview:", tags[:10])

