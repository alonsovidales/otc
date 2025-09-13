#!/usr/bin/env python3
import argparse
import os
from pathlib import Path
from huggingface_hub import snapshot_download

DEFAULT_MODELS = [
    "openai/clip-vit-base-patch32",   # lightest widely-used CLIP
    "openai/clip-vit-base-patch16",   # stronger, still compact
    # You can also add (heavier, higher quality):
    # "openai/clip-vit-large-patch14",
    # "openai/clip-vit-large-patch14-336",
    # "laion/CLIP-ViT-H-14-laion2B-s32B-b79K",
]

ALLOW_PATTERNS = [
    # tokenizer files (BPE)
    "vocab.json",
    "merges.txt",
    "tokenizer.json",
    "tokenizer_config.json",
    "special_tokens_map.json",
    "added_tokens.json",
    # image preproc + config
    "preprocessor_config.json",
    "config.json",
    # weights (either safetensors or bin)
    "*.safetensors",
    "pytorch_model.bin",
    # misc (some repos include these)
    "model_card.json",
    "README.*",
]

def repo_to_dirname(repo_id: str) -> str:
    return repo_id.replace("/", "__")

def main():
    ap = argparse.ArgumentParser(description="Download CLIP BPE models + tokenizers (vocab.json/merges.txt).")
    ap.add_argument(
        "--model", "-m", action="append", default=None,
        help="HF repo id (e.g., openai/clip-vit-base-patch32). Repeat for multiple."
    )
    ap.add_argument(
        "--out", default="models",
        help="Output root directory (default: ./models)"
    )
    args = ap.parse_args()

    models = args.model or DEFAULT_MODELS
    out_root = Path(args.out)
    out_root.mkdir(parents=True, exist_ok=True)

    print(f"Downloading to: {out_root.resolve()}")
    for repo in models:
        local_dir = out_root / repo_to_dirname(repo)
        print(f"\n==> {repo} -> {local_dir}")
        snapshot_download(
            repo_id=repo,
            local_dir=str(local_dir),
            local_dir_use_symlinks=False,
            allow_patterns=ALLOW_PATTERNS,
        )

        # sanity check: confirm BPE files exist
        vocab = local_dir / "vocab.json"
        merges = local_dir / "merges.txt"
        if vocab.exists() and merges.exists():
            print(f"   ✓ Found tokenizer files: {vocab.name}, {merges.name}")
        else:
            print("   ⚠️  Could not find vocab.json/merges.txt here. "
                  "Open the repo Files tab to verify they’re present for this model.")

        # show where weights landed
        safes = list(local_dir.glob("*.safetensors"))
        binf  = local_dir / "pytorch_model.bin"
        if safes:
            print(f"   ✓ Weights: {safes[0].name} (+ possibly sharded)")
        elif binf.exists():
            print(f"   ✓ Weights: {binf.name}")
        else:
            print("   ⚠️  No weights found in the filtered files (adjust ALLOW_PATTERNS if needed).")

        # remind about image preproc
        preproc = local_dir / "preprocessor_config.json"
        if preproc.exists():
            print(f"   ✓ Image preproc: {preproc.name}")
        else:
            print("   ⚠️  Missing preprocessor_config.json (rare).")

    print("\nDone. You can now load BPE with vocab/merges in Go or Python.")

if __name__ == "__main__":
    main()

