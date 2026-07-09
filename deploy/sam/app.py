"""SAM subject-select service — click-to-mask for the canvas.

NOT an inference-api endpoint: segmentation is an interactive tool
(sync, low-latency), not a generation task, so it gets a minimal API of
its own. The Iris API proxies CanvasService.SubjectMask here; the browser
never talks to this container.

POST /mask  {"image_url": ..., "points": [[x, y, label], ...]}  -> PNG
  label 1 = foreground (include), 0 = background (exclude).
  White = subject, black = elsewhere — same convention as inpaint masks,
  so the result feeds Remove/gen-fill unchanged.

The encoder (the expensive half, ~1-2s CPU) runs once per unique image —
embeddings are cached by content sha256, so presigned-URL churn doesn't
re-embed and every extra click is decoder-only (~30ms).
"""

import hashlib
import io
import json
import os
import threading
from collections import OrderedDict
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import numpy as np
import onnxruntime as ort
import requests
from PIL import Image

MODEL_DIR = os.environ.get("SAM_MODEL_DIR", "/models")
PORT = int(os.environ.get("PORT", "8900"))
MAX_INPUT_BYTES = 64 << 20
SIDE = 1024  # SAM's fixed long side
MEAN = np.array([123.675, 116.28, 103.53], dtype=np.float32)
STD = np.array([58.395, 57.12, 57.375], dtype=np.float32)

_lock = threading.Lock()
_enc = None
_dec = None
_enc_input = None
_cache: "OrderedDict[str, tuple]" = OrderedDict()  # sha -> (embedding, scale, (w,h))
CACHE_MAX = 8


def sessions():
    global _enc, _dec, _enc_input
    with _lock:
        if _enc is None:
            def find(suffix):
                for f in sorted(os.listdir(MODEL_DIR)):
                    if f.endswith(suffix):
                        return os.path.join(MODEL_DIR, f)
                raise FileNotFoundError(f"no *{suffix} in {MODEL_DIR}")

            _enc = ort.InferenceSession(find(".encoder.onnx"), providers=["CPUExecutionProvider"])
            _dec = ort.InferenceSession(find(".decoder.onnx"), providers=["CPUExecutionProvider"])
            _enc_input = _enc.get_inputs()[0].name
        return _enc, _dec, _enc_input


def embed(data: bytes):
    """Encoder pass with content-addressed caching. Handles both export
    styles: HWC dynamic-size inputs have preprocessing baked into the graph
    (anylabeling exports — feed the raw image); NCHW fixed-1024 inputs get
    the standard SAM resize/normalize/pad pipeline here."""
    sha = hashlib.sha256(data).hexdigest()
    with _lock:
        if sha in _cache:
            _cache.move_to_end(sha)
            return _cache[sha]
    img = Image.open(io.BytesIO(data)).convert("RGB")
    w, h = img.size
    scale = SIDE / max(w, h)
    enc, _, enc_input = sessions()
    shape = enc.get_inputs()[0].shape
    if len(shape) == 3:  # (H, W, 3) raw image, preprocessing in-graph
        tensor = np.asarray(img, dtype=np.float32)
    else:  # (1, 3, 1024, 1024) classic export
        rw, rh = round(w * scale), round(h * scale)
        resized = np.asarray(img.resize((rw, rh), Image.BILINEAR), dtype=np.float32)
        normed = (resized - MEAN) / STD
        padded = np.zeros((SIDE, SIDE, 3), dtype=np.float32)
        padded[:rh, :rw] = normed
        tensor = padded.transpose(2, 0, 1)[None]
    embedding = enc.run(None, {enc_input: tensor})[0]
    entry = (embedding, scale, (w, h))
    with _lock:
        _cache[sha] = entry
        while len(_cache) > CACHE_MAX:
            _cache.popitem(last=False)
    return entry


def predict_mask(data: bytes, points):
    embedding, scale, (w, h) = embed(data)
    _, dec, _ = sessions()
    coords = np.array([[[x * scale, y * scale] for x, y, _ in points]], dtype=np.float32)
    labels = np.array([[lbl for _, _, lbl in points]], dtype=np.float32)
    out = dec.run(
        None,
        {
            "image_embeddings": embedding,
            "point_coords": coords,
            "point_labels": labels,
            "mask_input": np.zeros((1, 1, 256, 256), dtype=np.float32),
            "has_mask_input": np.zeros(1, dtype=np.float32),
            "orig_im_size": np.array([h, w], dtype=np.float32),
        },
    )
    masks = out[0]  # (1, N, H, W) logits at original size
    mask = masks[0, 0] > 0
    img = Image.fromarray((mask * 255).astype(np.uint8), mode="L")
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):  # noqa: N802
        pass

    def send_json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):  # noqa: N802
        if self.path == "/healthz":
            self.send_response(200)
            self.end_headers()
            return
        self.send_json(404, {"error": "not found"})

    def do_POST(self):  # noqa: N802
        if not (self.headers.get("Authorization") or "").startswith("Bearer "):
            self.send_json(401, {"error": "missing bearer token"})
            return
        if self.path != "/mask":
            self.send_json(404, {"error": "not found"})
            return
        length = int(self.headers.get("Content-Length") or 0)
        if length > (1 << 20):
            self.send_json(400, {"error": "request too large"})
            return
        try:
            req = json.loads(self.rfile.read(length))
            url = req["image_url"]
            points = [(float(x), float(y), int(lbl)) for x, y, lbl in req["points"]]
            assert 1 <= len(points) <= 16
        except Exception:
            self.send_json(400, {"error": "bad request: need image_url and 1..16 [x,y,label] points"})
            return
        try:
            resp = requests.get(url, timeout=60, stream=True)
            resp.raise_for_status()
            data = resp.raw.read(MAX_INPUT_BYTES + 1, decode_content=True)
            if len(data) > MAX_INPUT_BYTES:
                raise ValueError("input exceeds 64MB")
            png = predict_mask(data, points)
        except Exception as e:
            self.send_json(502, {"error": f"segmentation failed: {e}"})
            return
        self.send_response(200)
        self.send_header("Content-Type", "image/png")
        self.send_header("Content-Length", str(len(png)))
        self.end_headers()
        self.wfile.write(png)


def warm():
    try:
        sessions()
        print("model loaded", flush=True)
    except Exception as e:
        print(f"FATAL: model load failed: {e}", flush=True)
        os._exit(1)


def main():
    threading.Thread(target=warm, daemon=True).start()
    print(f"sam listening on :{PORT} (models: {MODEL_DIR})", flush=True)
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()


if __name__ == "__main__":
    main()
