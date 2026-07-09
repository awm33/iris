"""LaMa removal endpoint — implements spec/inference-api.md.

A prompt-ignoring inpaint specialist (manifest features.prompt=false): the
fast local tier of the Remove tool. LaMa (Resolution-robust Large Mask
Inpainting, WACV 2022) runs via onnxruntime on CPU; the model is fetched
into /models on first start (see entrypoint.sh).

Python here is deliberate: this is the "Python where it earns it for AI"
case — the endpoint is dockerized, spec-conformant HTTP, and swappable for
a Go/ONNX implementation without anyone noticing (conformance is the
contract, including the mask_semantics check).

Job semantics per the spec: idempotent create, immutable terminal states,
cancel-responsive, artifacts PUT to the request's presigned targets with
sha256 reported. Inputs are inpainted crop-first (IOPaint-style): pad the
mask bbox, resize the crop to the model resolution, inpaint, paste back —
pixels outside the mask are copied from the source byte-for-byte.
"""

import hashlib
import io
import json
import os
import re
import threading
import time
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import numpy as np
import onnxruntime as ort
import requests
from PIL import Image

MODEL_PATH = os.environ.get("LAMA_MODEL_PATH", "/models/lama_fp32.onnx")
PORT = int(os.environ.get("PORT", "8900"))
MAX_INPUT_BYTES = 64 << 20

MANIFEST = {
    "spec_version": "1.0",
    "id": "lama-onnx",
    "family": "lama",
    "version": "1.0.0",
    "modality": "image",
    "tasks": ["inpaint"],
    "profiles": {
        # Crop-based: effective ceiling is memory, not the model.
        "draft": {"max_width": 8192, "max_height": 8192},
    },
    "references": {},
    "conditioning": {"mask": True, "source_image": True},
    "features": {
        "prompt": False,  # removal specialist: prompts are rejected, not ignored
        "seed": False,
        "negative_prompt": False,
    },
    "params_schema": {"type": "object", "properties": {}},
    "pricing": {"unit": "gpu_second", "estimates": {"draft": 0.02}},
    "limits": {"concurrency": 2, "max_queue": 16},
}

_session = None
_session_lock = threading.Lock()
_model_size = None  # (H, W) the export expects; None until loaded


def get_session():
    global _session, _model_size
    with _session_lock:
        if _session is None:
            sess = ort.InferenceSession(MODEL_PATH, providers=["CPUExecutionProvider"])
            shape = sess.get_inputs()[0].shape  # (N, 3, H, W)
            h = shape[2] if isinstance(shape[2], int) else 512
            w = shape[3] if isinstance(shape[3], int) else 512
            _session, _model_size = sess, (h, w)
        return _session, _model_size


def fetch_image(url, mode):
    resp = requests.get(url, timeout=60, stream=True)
    resp.raise_for_status()
    data = resp.raw.read(MAX_INPUT_BYTES + 1, decode_content=True)
    if len(data) > MAX_INPUT_BYTES:
        raise ValueError("input exceeds 64MB")
    return Image.open(io.BytesIO(data)).convert(mode)


def inpaint(src: Image.Image, mask: Image.Image) -> Image.Image:
    """Crop-first LaMa: pixels outside the mask are byte-preserved."""
    sess, (mh, mw) = get_session()
    if mask.size != src.size:
        mask = mask.resize(src.size, Image.NEAREST)
    mask_np = (np.asarray(mask) > 127).astype(np.uint8)
    ys, xs = np.nonzero(mask_np)
    if len(xs) == 0:
        return src  # nothing to remove

    # Pad the bbox for context; clamp to the image.
    x0, x1, y0, y1 = xs.min(), xs.max() + 1, ys.min(), ys.max() + 1
    pad = max(32, (max(x1 - x0, y1 - y0) * 3) // 10)
    cx0, cy0 = max(0, x0 - pad), max(0, y0 - pad)
    cx1, cy1 = min(src.width, x1 + pad), min(src.height, y1 + pad)

    crop = src.crop((cx0, cy0, cx1, cy1)).resize((mw, mh), Image.BILINEAR)
    mcrop = Image.fromarray(mask_np[cy0:cy1, cx0:cx1] * 255).resize((mw, mh), Image.NEAREST)

    img_t = np.asarray(crop, dtype=np.float32).transpose(2, 0, 1)[None] / 255.0
    mask_t = (np.asarray(mcrop, dtype=np.float32)[None, None] > 127).astype(np.float32)
    names = [i.name for i in sess.get_inputs()]
    out = sess.run(None, {names[0]: img_t, names[1]: mask_t})[0][0]

    if out.max() <= 1.5:  # exports vary: [0,1] vs [0,255]
        out = out * 255.0
    out_img = Image.fromarray(np.clip(out.transpose(1, 2, 0), 0, 255).astype(np.uint8))
    out_img = out_img.resize((cx1 - cx0, cy1 - cy0), Image.BILINEAR)

    # Paste back ONLY the masked pixels (spec: black regions byte-preserved).
    result = src.copy()
    region_mask = Image.fromarray(mask_np[cy0:cy1, cx0:cx1] * 255)
    result.paste(out_img, (cx0, cy0), region_mask)
    return result


jobs = {}
jobs_lock = threading.Lock()


def fail(job, code, message, retryable):
    with jobs_lock:
        if job["state"] not in ("canceled",):
            job.update(state="failed", error={"code": code, "message": message, "retryable": retryable})


def run_job(job, req):
    started = time.time()
    with jobs_lock:
        if job["state"] == "canceled":
            return
        job["state"] = "running"
        job["progress"] = 0.1
    try:
        cond = req.get("conditioning") or {}
        src = fetch_image(cond["source_image"]["url"], "RGB")
        mask = fetch_image(cond["mask"]["url"], "L")
    except Exception as e:  # caller's inputs: invalid_input, no retry
        fail(job, "invalid_input", f"conditioning input: {e}", False)
        return
    with jobs_lock:
        if job["state"] == "canceled":
            return
        job["progress"] = 0.4
    try:
        result = inpaint(src, mask)
    except Exception as e:
        fail(job, "internal", f"inference: {e}\n{traceback.format_exc(limit=3)}", True)
        return
    with jobs_lock:
        if job["state"] == "canceled":
            return
        job["state"] = "uploading"
        job["progress"] = 0.9

    buf = io.BytesIO()
    result.save(buf, format="PNG")
    data = buf.getvalue()
    sha = hashlib.sha256(data).hexdigest()
    artifact = {
        "index": 0,
        "content_type": "image/png",
        "width": result.width,
        "height": result.height,
        "uploaded": False,
        "sha256": sha,
        "safety": {"flagged": False},
    }
    targets = (req.get("upload") or {}).get("artifacts") or []
    if targets and targets[0].get("put_url"):
        try:
            put = requests.put(targets[0]["put_url"], data=data, headers={"Content-Type": "image/png"}, timeout=120)
            put.raise_for_status()
            artifact["uploaded"] = True
        except Exception as e:
            fail(job, "internal", f"artifact upload: {e}", True)
            return
    with jobs_lock:
        if job["state"] == "canceled":
            return
        job.update(
            state="complete",
            progress=1.0,
            artifacts=[artifact],
            metrics={"gpu_seconds": round(time.time() - started, 2)},
        )


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):  # noqa: N802 — quiet, docker logs get status lines below
        pass

    def send_json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def authed(self):
        if not (self.headers.get("Authorization") or "").startswith("Bearer "):
            self.send_json(401, {"error": {"code": "invalid_input", "message": "missing bearer token", "retryable": False}})
            return False
        return True

    def do_GET(self):  # noqa: N802
        if self.path == "/v1/healthz":
            self.send_response(200)
            self.end_headers()
            return
        if not self.authed():
            return
        if self.path == "/v1/manifest":
            self.send_json(200, MANIFEST)
            return
        m = re.fullmatch(r"/v1/jobs/([\w.-]+)", self.path)
        if m:
            with jobs_lock:
                job = jobs.get(m.group(1))
                payload = dict(job) if job else None
            if payload is None:
                self.send_json(404, {"error": {"code": "invalid_input", "message": "unknown job", "retryable": False}})
            else:
                self.send_json(200, payload)
            return
        self.send_json(404, {"error": {"code": "invalid_input", "message": "not found", "retryable": False}})

    def do_POST(self):  # noqa: N802
        if not self.authed():
            return
        if self.path != "/v1/jobs":
            self.send_json(404, {"error": {"code": "invalid_input", "message": "not found", "retryable": False}})
            return
        length = int(self.headers.get("Content-Length") or 0)
        try:
            req = json.loads(self.rfile.read(length))
        except Exception:
            self.send_json(400, {"error": {"code": "invalid_input", "message": "bad json", "retryable": False}})
            return
        jid = req.get("id")
        if not jid:
            self.send_json(400, {"error": {"code": "invalid_input", "message": "missing id", "retryable": False}})
            return
        # Undeclared capabilities are rejected, never ignored (spec §2):
        # this endpoint does not condition on prompts.
        if req.get("task") != "inpaint":
            self.send_json(400, {"error": {"code": "invalid_input", "message": "only task 'inpaint' is supported", "retryable": False, "detail": {"capability": "tasks"}}})
            return
        if req.get("prompt"):
            self.send_json(400, {"error": {"code": "invalid_input", "message": "this endpoint ignores prompts (features.prompt=false) — submit without one", "retryable": False, "detail": {"capability": "features.prompt"}}})
            return
        cond = req.get("conditioning") or {}
        if not (cond.get("source_image") or {}).get("url") or not (cond.get("mask") or {}).get("url"):
            self.send_json(400, {"error": {"code": "invalid_input", "message": "inpaint requires conditioning.source_image and conditioning.mask", "retryable": False}})
            return
        with jobs_lock:
            if jid in jobs:  # idempotent create
                self.send_json(200, dict(jobs[jid]))
                return
            job = {"id": jid, "state": "queued", "progress": 0.0, "eta_s": None, "artifacts": None, "error": None, "metrics": None}
            jobs[jid] = job
        threading.Thread(target=run_job, args=(job, req), daemon=True).start()
        self.send_json(202, {"id": jid, "state": "queued", "queue_position": 0})

    def do_DELETE(self):  # noqa: N802
        if not self.authed():
            return
        m = re.fullmatch(r"/v1/jobs/([\w.-]+)", self.path)
        if not m:
            self.send_json(404, {"error": {"code": "invalid_input", "message": "not found", "retryable": False}})
            return
        with jobs_lock:
            job = jobs.get(m.group(1))
            if job is None:
                self.send_json(404, {"error": {"code": "invalid_input", "message": "unknown job", "retryable": False}})
                return
            if job["state"] in ("queued", "running", "uploading"):
                job["state"] = "canceled"
            self.send_json(200, dict(job))


def main():
    # Warm the model off the request path; healthz is up immediately and the
    # first job simply waits on the session lock if it races the load.
    threading.Thread(target=get_session, daemon=True).start()
    print(f"lama-onnx listening on :{PORT} (model: {MODEL_PATH})", flush=True)
    ThreadingHTTPServer(("0.0.0.0", PORT), Handler).serve_forever()


if __name__ == "__main__":
    main()
