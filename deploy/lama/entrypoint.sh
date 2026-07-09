#!/bin/sh
# Fetch LaMa ONNX weights on first start (cached in the /models volume).
# big-lama is Apache-2.0 (github.com/advimman/lama); this export is the
# widely-used Carve conversion.
set -e
MODEL_PATH="${LAMA_MODEL_PATH:-/models/lama_fp32.onnx}"
MODEL_URL="${LAMA_MODEL_URL:-https://huggingface.co/Carve/LaMa-ONNX/resolve/main/lama_fp32.onnx}"

if [ ! -s "$MODEL_PATH" ]; then
  echo "downloading LaMa weights → $MODEL_PATH"
  mkdir -p "$(dirname "$MODEL_PATH")"
  python - <<EOF
import os, sys, urllib.request
url = os.environ.get("LAMA_MODEL_URL", "$MODEL_URL")
dst = "$MODEL_PATH"
tmp = dst + ".part"
urllib.request.urlretrieve(url, tmp)
os.replace(tmp, dst)
print("downloaded", os.path.getsize(dst), "bytes")
EOF
fi

exec python app.py
