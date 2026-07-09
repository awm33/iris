#!/bin/sh
# Fetch LaMa ONNX weights on first start (cached in the /models volume).
# big-lama is Apache-2.0 (github.com/advimman/lama); this export is the
# widely-used Carve conversion. The sha256 pin makes the mutable HF ref
# irrelevant: onnxruntime parses whatever this file is, so its integrity is
# verified before it is ever loaded.
set -e
MODEL_PATH="${LAMA_MODEL_PATH:-/models/lama_fp32.onnx}"
MODEL_URL="${LAMA_MODEL_URL:-https://huggingface.co/Carve/LaMa-ONNX/resolve/main/lama_fp32.onnx}"
MODEL_SHA256="${LAMA_MODEL_SHA256:-1faef5301d78db7dda502fe59966957ec4b79dd64e16f03ed96913c7a4eb68d6}"

python - <<EOF
import hashlib, os, sys, urllib.request

dst = "$MODEL_PATH"
want = "$MODEL_SHA256"

def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()

if not (os.path.exists(dst) and os.path.getsize(dst) > 0):
    print("downloading LaMa weights ->", dst, flush=True)
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    tmp = dst + ".part"
    urllib.request.urlretrieve("$MODEL_URL", tmp)
    os.replace(tmp, dst)

got = sha256(dst)
if want and got != want:
    print(f"FATAL: model checksum mismatch: got {got}, want {want}", flush=True)
    os.remove(dst)
    sys.exit(1)
print("model verified,", os.path.getsize(dst), "bytes", flush=True)
EOF

exec python app.py
