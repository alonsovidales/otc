#!/usr/bin/env python3
"""
download.py  —  RAM++ (Swin-Large 14M) downloader + ONNX exporter (inference-only)

Usage:
  python download.py models/ram_plus --imgsz 384 --classes 4585 [--quantize] [--dynamic-spatial]

Prereqs (inside a clean venv, Python 3.10/3.11 recommended):
  pip install --upgrade pip wheel setuptools
  pip install huggingface_hub
  pip install torch --index-url https://download.pytorch.org/whl/cpu
  pip install timm==0.4.12 transformers==4.25.1 fairscale==0.4.4
  pip install "git+https://github.com/xinyu1205/recognize-anything.git"
  # if needed by your env:
  pip install "scipy>=1.10,<1.13" yacs einops

What this script does:
  • Downloads RAM++ checkpoints (.pth) from HF (anonymous).
  • Builds the model via ram_plus factory (Swin-L).
  • Wraps it with an inference-only module (no training args).
  • Exports ONNX: input "images"[1,3,384,384] → output "probs"[1,4585].
  • Optionally quantizes to INT8 (dynamic) if --quantize is passed.

License notes: RAM/RAM++ code & models are Apache-2.0 (see authors’ repos & model cards).
"""

from __future__ import annotations
import argparse, sys
from pathlib import Path

# ---- Transformers compatibility shim (for newer installs) ----
try:
    from transformers.modeling_utils import apply_chunking_to_forward  # noqa
except Exception:
    try:
        import transformers
        from transformers import pytorch_utils as _ptu
        # alias back for older-code imports used by RAM package
        transformers.modeling_utils.apply_chunking_to_forward = _ptu.apply_chunking_to_forward  # type: ignore
    except Exception:
        pass

from huggingface_hub import snapshot_download

import torch
import torch.nn as nn

# Import RAM after the shim
try:
    from ram.models import ram_plus
    from ram import get_transform
except Exception as e:
    print("ERROR: Could not import the 'ram' package.", file=sys.stderr)
    print('Install it inside your venv:', file=sys.stderr)
    print('  pip install "git+https://github.com/xinyu1205/recognize-anything.git"', file=sys.stderr)
    print("Original import error:", e, file=sys.stderr)
    sys.exit(1)


def human_size(n: int) -> str:
    units = ["B", "KB", "MB", "GB", "TB"]
    i = 0
    f = float(n)
    while f >= 1024 and i < len(units) - 1:
        f /= 1024.0
        i += 1
    return f"{f:.1f} {units[i]}"


def pick_checkpoint(weights_dir: Path) -> Path:
    cands = sorted(weights_dir.rglob("*.pth"), key=lambda p: p.stat().st_size, reverse=True)
    if not cands:
        raise FileNotFoundError(f"No .pth files under {weights_dir}")
    # Prefer RAM++ Swin-L names if present
    prefer = [p for p in cands if "ram_plus" in p.name and "swin" in p.name and "large" in p.name]
    return prefer[0] if prefer else cands[0]


class RAMPlusExport(nn.Module):
    """
    Inference-only wrapper mirroring the project's batch inference path:
    images [B,3,H,W] (Imagenet normalized) -> probs [B, num_tags]
    """
    def __init__(self, base: nn.Module):
        super().__init__()
        # Grab needed submodules/params from the RAM++ model
        # Attribute names are consistent in official repo
        self.visual_encoder = base.visual_encoder
        self.image_proj     = base.image_proj
        self.tagging_head   = base.tagging_head
        self.wordvec_proj   = base.wordvec_proj
        self.fc             = base.fc
        self.reweight_scale = base.reweight_scale
        self.label_embed    = base.label_embed      # [num_tags * des_per_class, 512]
        self.num_class      = int(base.num_class)   # 4585
        # cache derived shapes
        self._desc_per_cls  = self.label_embed.shape[0] // self.num_class

    def forward(self, images: torch.Tensor) -> torch.Tensor:
        # 1) Visual encoder + projection
        image_embeds = self.image_proj(self.visual_encoder(images))   # [B, T, C]
        image_atts = torch.ones(image_embeds.size()[:-1],
                                dtype=torch.long, device=images.device)  # [B, T]

        # 2) Reweight label embeddings per image (RAM++ trick)
        B = image_embeds.shape[0]
        D = self._desc_per_cls
        cls = image_embeds[:, 0, :]
        cls = cls / (cls.norm(dim=-1, keepdim=True) + 1e-12)

        logits_per_image = (self.reweight_scale.exp() * (cls @ self.label_embed.t()))  # [B, N*D]
        logits_per_image = logits_per_image.view(B, self.num_class, D)                 # [B, N, D]

        weight_norm = torch.softmax(logits_per_image, dim=2)                           # [B, N, D]
        label_embed_reshaped = self.label_embed.view(self.num_class, D, -1)            # [N, D, 512]
        # einsum: [B,N,D] x [N,D,512] -> [B,N,512]
        label_embed = torch.einsum('bnd,ndh->bnh', weight_norm, label_embed_reshaped)
        label_embed = torch.relu(self.wordvec_proj(label_embed))                       # [B,N,H]

        # 3) Tagging head + final FC + sigmoid
        tagging_embed, _ = self.tagging_head(
            encoder_embeds=label_embed,
            encoder_hidden_states=image_embeds,
            encoder_attention_mask=image_atts,
            return_dict=False,
            mode='tagging',
        )                                                                               # [B,N,H]
        logits = self.fc(tagging_embed).squeeze(-1)                                     # [B,N]
        probs  = torch.sigmoid(logits)
        return probs


def export_onnx_inference(model: nn.Module, onnx_path: Path, img_size: int,
                          dynamic_spatial: bool = False):
    onnx_path.parent.mkdir(parents=True, exist_ok=True)
    model.eval()
    dummy = torch.zeros(1, 3, img_size, img_size, dtype=torch.float32)
    dyn_axes = {"images": {0: "batch"}, "probs": {0: "batch"}}
    if dynamic_spatial:
        dyn_axes["images"].update({2: "h", 3: "w"})
    torch.onnx.export(
        model, dummy, str(onnx_path),
        input_names=["images"], output_names=["probs"],
        dynamic_axes=dyn_axes,
        opset_version=17, do_constant_folding=True,
    )


def maybe_quantize(in_path: Path, out_path: Path):
    try:
        from onnxruntime.quantization import quantize_dynamic, QuantType
    except Exception:
        print("Quantization tools not available; skipping INT8.", file=sys.stderr)
        return
    out_path.parent.mkdir(parents=True, exist_ok=True)
    quantize_dynamic(
        model_input=str(in_path),
        model_output=str(out_path),
        op_types_to_quantize=["MatMul", "Attention", "Gemm", "Conv"],
        weight_type=QuantType.QInt8,
    )


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("target_dir", nargs="?", default="models/ram_plus",
                    help="Target folder (default: models/ram_plus)")
    ap.add_argument("--imgsz", type=int, default=384, choices=[384, 512],
                    help="Export image size (default: 384)")
    ap.add_argument("--classes", type=int, default=4585,
                    help="Number of tags/classes (info only; model carries this)")
    ap.add_argument("--quantize", action="store_true",
                    help="Also write an INT8-quantized ONNX")
    ap.add_argument("--dynamic-spatial", action="store_true",
                    help="Export ONNX with dynamic H/W axes (default: fixed H=W=imgsz)")
    args = ap.parse_args()

    base = Path(args.target_dir).resolve()
    weights_dir = base / "weights"
    onnx_dir = base / "onnx"

    print(f"Target dir: {base}")

    # 1) Download checkpoints (anonymous)
    print("Downloading RAM++ weights from HF …")
    repo_id = "xinyu1205/recognize-anything-plus-model"
    local = snapshot_download(
        repo_id=repo_id,
        local_dir=str(weights_dir),
        allow_patterns=["*.pth"],
        token=None,
    )
    weights_dir = Path(local)
    ckpt = pick_checkpoint(weights_dir)
    print(f"Checkpoint: {ckpt}  ({human_size(ckpt.stat().st_size)})")

    # 2) Build model via factory (Swin-Large), using the checkpoint
    print(f"Building RAM++ (Swin-Large), image_size={args.imgsz} …")
    base_model = ram_plus(
        pretrained=str(ckpt),
        image_size=args.imgsz,
        vit="swin_l",
    ).eval()

    # 3) Wrap with inference-only module (image -> probs)
    wrapped = RAMPlusExport(base_model).eval()

    # 4) Export ONNX
    onnx_fp32 = onnx_dir / "ram_plus_swin_large_14m.onnx"
    print(f"Exporting ONNX → {onnx_fp32} …")
    export_onnx_inference(
        wrapped, onnx_fp32, img_size=args.imgsz,
        dynamic_spatial=args.dynamic_spatial
    )

    # 5) Optional INT8 quant
    if args.quantize:
        onnx_int8 = onnx_dir / "ram_plus_swin_large_14m.int8.onnx"
        print(f"Quantizing (dynamic INT8) → {onnx_int8} …")
        maybe_quantize(onnx_fp32, onnx_int8)

    print("\nDone.")
    print(f"ONNX fp32: {onnx_fp32.resolve()}  ({human_size(onnx_fp32.stat().st_size)})")
    if args.quantize:
        q = onnx_dir / "ram_plus_swin_large_14m.int8.onnx"
        if q.exists():
            print(f"ONNX int8: {q.resolve()}  ({human_size(q.stat().st_size)})")
    print("\nONNX I/O:")
    print("  input  name=images  shape=[1,3,{sz},{sz}]  dtype=float32 (Imagenet mean/std)".format(sz=args.imgsz))
    print("  output name=probs   shape=[1,4585]         dtype=float32 (multi-label probabilities)")
    print("\nTip: in Go, threshold probs (e.g., 0.35–0.45) and map indices to your tag_list_4585.txt")

if __name__ == "__main__":
    torch.set_grad_enabled(False)
    main()

