#!/bin/sh
# Fetch MobileSAM ONNX weights (encoder+decoder zip) on first start.
# MobileSAM is Apache-2.0 (github.com/ChaoningZhang/MobileSAM); this export
# is from the anylabeling model zoo. sha256-pinned: integrity is verified
# before onnxruntime ever parses the files.
set -e
MODEL_DIR="${SAM_MODEL_DIR:-/models}"
ZIP_URL="${SAM_MODEL_URL:-https://huggingface.co/vietanhdev/segment-anything-onnx-models/resolve/main/mobile_sam_20230629.zip}"
ZIP_SHA256="${SAM_MODEL_SHA256:-41aff2660b7531becfee21fb257c49933ddc892c554507bdb775bf504d443942}"

python - <<EOF
import hashlib, os, sys, urllib.request, zipfile

d = "$MODEL_DIR"
os.makedirs(d, exist_ok=True)
have = [f for f in os.listdir(d) if f.endswith(".encoder.onnx")] and \
       [f for f in os.listdir(d) if f.endswith(".decoder.onnx")]
if not have:
    tmp = os.path.join(d, "sam.zip.part")
    print("downloading MobileSAM weights", flush=True)
    urllib.request.urlretrieve("$ZIP_URL", tmp)
    h = hashlib.sha256()
    with open(tmp, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    got = h.hexdigest()
    want = "$ZIP_SHA256"
    print("zip sha256:", got, flush=True)
    if want and got != want:
        print(f"FATAL: checksum mismatch: want {want}", flush=True)
        os.remove(tmp)
        sys.exit(1)
    with zipfile.ZipFile(tmp) as z:
        for name in z.namelist():
            if name.endswith(".onnx") and "/" not in name.strip("/"):
                z.extract(name, d)
    os.remove(tmp)
print("models:", [f for f in os.listdir(d) if f.endswith(".onnx")], flush=True)
EOF

exec python app.py
