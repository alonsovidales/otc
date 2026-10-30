#!/usr/bin/env python3
"""
siglip2_export.py  —  SigLIP 2 for Smart search (images_embedder): export
the image and text towers to ONNX, quantize them, and check them against
the published model.

Usage:
  python -I siglip2_export.py MODEL_DIR OUT_DIR [--images DIR]

  MODEL_DIR  a download of google/siglip2-base-patch16-256 (Apache-2.0), the
             safetensors weights only:
               hf download google/siglip2-base-patch16-256 model.safetensors \
                 config.json preprocessor_config.json tokenizer.model \
                 tokenizer.json tokenizer_config.json special_tokens_map.json \
                 --local-dir MODEL_DIR
  OUT_DIR    gets image.onnx + image.onnx.data, text.onnx + text.onnx.data and
             tokenizer.model: what the device loads.
  --images   a folder of JPEGs to check the image tower on (else synthetic
             pictures are used).

Prereqs (a clean venv):
  pip install torch transformers sentencepiece protobuf safetensors onnx \
      onnx-ir "onnxruntime==1.24.3" pillow numpy

What it does:
  • Exports both towers (the pooled, unnormalised features) with
    torch.onnx.export, opset 17: pixel_values [N,3,256,256] -> embeds
    [N,768], input_ids [N,64] -> embeds [N,768].
  • Image tower: 8-bit weights in blocks of 32, int8 compute (MatMulNBits,
    accuracy level 4). Plain dynamic int8 also quantizes the ViT's
    activations and fell to a cosine of 0.90 against fp32.
  • Text tower: the 256k-token table to int8 with one scale per token,
    looked up as Gather(int8) -> Cast -> Mul(Gather(scales)), and the
    weights as above. Dynamic quantization puts a DequantizeLinear on the
    table itself, which ONNX Runtime folds back to fp32 at load (~800 MB
    resident); nothing here is constant, so it stays one byte a number.
  • Saves the weights in a .data file next to each model: ONNX Runtime
    memory-maps it, so the text tower touches only the rows a query uses
    (~130 MB resident instead of ~800 MB).
  • Checks every tower against transformers' fp32 model: cosine >= 0.99
    on every image and text, or it fails.
"""

from __future__ import annotations

import argparse
import glob
import hashlib
import os
import shutil
import sys
import tempfile

import numpy as np
import onnx
import onnxruntime as ort
import torch
from onnx import TensorProto, helper, numpy_helper
from onnxruntime.quantization.matmul_nbits_quantizer import (
    DefaultWeightOnlyQuantConfig,
    MatMulNBitsQuantizer,
)
from PIL import Image
from transformers import AutoProcessor, AutoTokenizer, SiglipModel

TEXTS = [
    "a dog on the beach", "un perro en la playa", "un chien dans la neige",
    "ein Kind fährt Fahrrad", "un tramonto sul mare", "bolo de aniversário com velas",
    "kinderen spelen in de herfstbladeren", "christmas dinner with the grandparents",
]


def pooled(out):
    return out if isinstance(out, torch.Tensor) else out.pooler_output


class Vision(torch.nn.Module):
    def __init__(self, m):
        super().__init__()
        self.m = m

    def forward(self, pixel_values):
        return pooled(self.m.get_image_features(pixel_values=pixel_values))


class Text(torch.nn.Module):
    def __init__(self, m):
        super().__init__()
        self.m = m

    def forward(self, input_ids):
        return pooled(self.m.get_text_features(input_ids=input_ids))


def export(module, example, path, name):
    torch.onnx.export(
        module, (example[:1],), path, input_names=[name], output_names=["embeds"],
        dynamic_axes={name: {0: "batch"}, "embeds": {0: "batch"}},
        opset_version=17, dynamo=False,
    )


def nbits(model):
    cfg = DefaultWeightOnlyQuantConfig(
        block_size=32, is_symmetric=True, accuracy_level=4, bits=8,
        op_types_to_quantize=("MatMul",), quant_axes=(("MatMul", 0),),
    )
    q = MatMulNBitsQuantizer(model, algo_config=cfg)
    q.process()
    return q.model.model


def int8_token_table(model):
    g = model.graph
    inits = {t.name: t for t in g.initializer}
    node = next(n for n in g.node if n.op_type == "Gather" and n.input[0] in inits
                and len(inits[n.input[0]].dims) == 2 and inits[n.input[0]].dims[0] > 100000)
    w = numpy_helper.to_array(inits[node.input[0]]).astype(np.float32)
    scale = np.abs(w).max(axis=1, keepdims=True) / 127.0
    scale[scale == 0] = 1.0
    wq = np.clip(np.round(w / scale), -127, 127).astype(np.int8)
    base, ids, out = node.input[0], node.input[1], node.output[0]
    g.initializer.remove(inits[base])
    g.initializer.extend([numpy_helper.from_array(wq, base + "_int8"),
                          numpy_helper.from_array(scale.astype(np.float32), base + "_scale")])
    new = [
        helper.make_node("Gather", [base + "_int8", ids], [out + "_q"], name=node.name + "_q", axis=0),
        helper.make_node("Cast", [out + "_q"], [out + "_f"], name=node.name + "_cast", to=TensorProto.FLOAT),
        helper.make_node("Gather", [base + "_scale", ids], [out + "_s"], name=node.name + "_scale", axis=0),
        helper.make_node("Mul", [out + "_f", out + "_s"], [out], name=node.name + "_mul"),
    ]
    i = list(g.node).index(node)
    g.node.remove(node)
    for k, n in enumerate(new):
        g.node.insert(i + k, n)
    return model


def save(model, path):
    onnx.save(model, path, save_as_external_data=True, all_tensors_to_one_file=True,
              location=os.path.basename(path) + ".data", size_threshold=1024)


def cosines(a, b):
    a = a / np.linalg.norm(a, axis=1, keepdims=True)
    b = b / np.linalg.norm(b, axis=1, keepdims=True)
    return (a * b).sum(1)


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("model_dir")
    ap.add_argument("out_dir")
    ap.add_argument("--images")
    a = ap.parse_args()
    os.makedirs(a.out_dir, exist_ok=True)

    model = SiglipModel.from_pretrained(a.model_dir, torch_dtype=torch.float32).eval()
    proc = AutoProcessor.from_pretrained(a.model_dir)
    tok = AutoTokenizer.from_pretrained(a.model_dir)

    if a.images:
        paths = sorted(glob.glob(os.path.join(a.images, "**", "*.jpg"), recursive=True))[:16]
        pictures = [Image.open(p).convert("RGB") for p in paths]
    else:
        rng = np.random.default_rng(0)
        pictures = [Image.fromarray(rng.integers(0, 255, (300, 400, 3), dtype=np.uint8)) for _ in range(4)]
    pix = proc(images=pictures, return_tensors="pt")["pixel_values"]
    # SigLIP 2 was trained on lowercased text padded to 64 tokens.
    ids = tok([t.lower() for t in TEXTS], padding="max_length", max_length=64, truncation=True,
              return_tensors="pt")["input_ids"]
    with torch.no_grad():
        ref_img = Vision(model)(pix).numpy()
        ref_txt = Text(model)(ids).numpy()

    with tempfile.TemporaryDirectory() as work:
        with torch.no_grad():
            export(Vision(model), pix, os.path.join(work, "image.onnx"), "pixel_values")
            export(Text(model), ids, os.path.join(work, "text.onnx"), "input_ids")
        image = nbits(onnx.load(os.path.join(work, "image.onnx"), load_external_data=True))
        save(image, os.path.join(a.out_dir, "image.onnx"))
        text = nbits(int8_token_table(onnx.load(os.path.join(work, "text.onnx"), load_external_data=True)))
        save(text, os.path.join(a.out_dir, "text.onnx"))
    shutil.copyfile(os.path.join(a.model_dir, "tokenizer.model"), os.path.join(a.out_dir, "tokenizer.model"))

    ok = True
    for name, inp, arr, ref in (("image", "pixel_values", pix.numpy(), ref_img),
                                ("text", "input_ids", ids.numpy().astype(np.int64), ref_txt)):
        s = ort.InferenceSession(os.path.join(a.out_dir, name + ".onnx"), providers=["CPUExecutionProvider"])
        c = cosines(s.run(None, {inp: arr})[0], ref)
        print(f"{name}: cosine against fp32 min {c.min():.4f} mean {c.mean():.4f}")
        ok = ok and c.min() >= 0.99
    for f in ("image.onnx", "image.onnx.data", "text.onnx", "text.onnx.data", "tokenizer.model"):
        p = os.path.join(a.out_dir, f)
        print(f"{f}: {os.path.getsize(p)} bytes, sha256 {sha256(p)}")
    if not ok:
        sys.exit("a tower is further than a cosine of 0.99 from the published model")


if __name__ == "__main__":
    main()
