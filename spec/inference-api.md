# Iris Inference API — v1.0-draft

The HTTP contract every model endpoint implements to be usable from Iris: our Wan/Qwen-derived servers (R&D repo), other self-hosted open-weight servers, and the thin shims Iris writes around commercial APIs. One contract, one adapter core.

**Design goals:** trivially implementable behind any serving stack (FastAPI, Go, Triton wrapper); storage-agnostic (endpoints never touch Iris's buckets directly — signed URLs both ways); async-first (video jobs run minutes); honest capabilities (everything the UI offers is declared in the manifest).

**Conformance:** `backend/cmd/mock-model` is the reference implementation; `backend/internal/conformance` is the test suite. An endpoint that passes conformance works in Iris — that's the definition.

---

## 1. Overview

```
GET    /v1/manifest            → capability manifest (JSON; schema: spec/manifest.schema.json)
POST   /v1/jobs                → create a generation job (async)
GET    /v1/jobs/{id}           → job status + progress
DELETE /v1/jobs/{id}           → cancel
GET    /v1/healthz             → liveness (200 = accepting jobs)
```

- **Auth:** `Authorization: Bearer <token>` on every request. Token issuance is out of band (config). Endpoints MUST reject unauthenticated requests (401).
- **Versioning:** the manifest carries `spec_version: "1.0"`. Breaking changes bump the major; Iris negotiates by reading the manifest first.
- **One job = one artifact set** for a single generation. **Fan-out (N takes) is orchestrator-side** — Iris submits N jobs. Endpoints that batch efficiently MAY declare `features.native_batch` and accept `n > 1`, returning N artifact sets; otherwise reject `n > 1` with `invalid_input`.

## 2. Create job — `POST /v1/jobs`

```jsonc
{
  "id": "j_9f2c...",                    // client-generated ULID; idempotency key (repeat POST with same id → same job, 200)
  "task": "t2v",                        // one of manifest.tasks: t2i | i2i | t2v | i2v | v2v | inpaint | outpaint | upscale | lipsync_post | ...
  "profile": "draft",                   // manifest.profiles key (draft | master | ...)
  "prompt": "Mara slides the plate across the counter...",
  "negative_prompt": null,              // optional; only if manifest.features.negative_prompt
  "seed": 123456789,                    // optional; endpoints MUST honor it deterministically if manifest.features.seed
  "output": { "width": 1280, "height": 720, "duration_s": 6.0, "fps": 24 },  // duration/fps video-only; must fit manifest limits
  "references": [                       // each entry maps to a declared manifest.references slot
    { "kind": "image", "role": "character",  "url": "https://...signed...", "weight": 1.0 },
    { "kind": "image", "role": "scene_view", "url": "https://...signed..." },
    { "kind": "audio", "role": "speech_lipsync", "url": "https://...signed..." }
  ],
  "conditioning": {                     // all optional; each key requires the matching manifest.conditioning flag
    "first_frame":   { "url": "https://...signed..." },
    "last_frame":    { "url": "https://...signed..." },
    "keyframes":     [ { "t": 0.0, "url": "..." }, { "t": 3.5, "url": "..." } ],   // multi-keyframe (superset of first/last)
    "depth_sequence":{ "url": "https://...zip-or-video..." },                      // per-frame depth maps
    "source_video":  { "url": "https://...", "strength": 0.6 },                    // v2v / restyle / extension input
    "mask":          { "url": "https://..." }                                      // inpaint (image: static; video: mask video)
  },
  "params": { },                        // model-specific; validated against manifest.params_schema (JSON Schema)
  "upload": {                           // Iris-provided presigned PUT targets; endpoint uploads artifacts here
    "artifacts": [ { "put_url": "https://...", "content_type": "video/mp4" } ],
    "thumbnail":   { "put_url": "https://...", "content_type": "image/jpeg" }      // optional
  },
  "webhook": { "url": "https://iris.../v1/callbacks/j_9f2c", "secret": "..." }     // optional; polling is the baseline
}
```

**Response `202`:** `{ "id": "j_9f2c...", "state": "queued", "queue_position": 3, "estimated_start_s": 40 }`

Validation failures → `400` with the error object (§4). Requests using undeclared capabilities MUST be rejected (`invalid_input`, `detail.capability` naming the missing flag) — never silently ignored. *(Iris pre-validates against the manifest; this is defense in depth.)*

## 3. Status — `GET /v1/jobs/{id}`

```jsonc
{
  "id": "j_9f2c...",
  "state": "running",                   // queued | running | uploading | complete | failed | canceled
  "progress": 0.42,                     // 0..1, best effort
  "eta_s": 95,                          // best effort, nullable
  "artifacts": null,                    // present when complete — see below
  "error": null,                       
  "metrics": { "gpu_seconds": 210.5 }   // present when terminal; basis for cost metering
}
```

On `complete`:

```jsonc
"artifacts": [{
  "index": 0,
  "content_type": "video/mp4",
  "width": 1280, "height": 720, "duration_s": 6.0, "fps": 24,
  "uploaded": true,                     // artifact was PUT to upload.artifacts[index].put_url
  "sha256": "ab34...",                  // integrity check
  "safety": { "flagged": false }        // endpoint-side safety result, if any
}]
```

Terminal states are immutable and MUST be retrievable for ≥24h. Webhook (if configured) POSTs the same status object on every state transition, HMAC-signed (`X-Iris-Signature: sha256=...` over the body with `webhook.secret`); Iris still polls as backstop.

## 4. Errors

```jsonc
{ "error": { "code": "safety_blocked", "message": "prompt rejected by policy", "retryable": false, "detail": { } } }
```

| code | meaning | orchestrator behavior |
|---|---|---|
| `invalid_input` | bad request / undeclared capability / limits exceeded | fail job, surface to user, no retry |
| `safety_blocked` | content policy (input or output) | fail with policy messaging, no retry |
| `transient` | OOM, worker died, timeout — try again | retry w/ backoff (≤3), then fail |
| `overloaded` | queue full — includes `retry_after_s` | requeue orchestrator-side |
| `internal` | endpoint bug | retry once, then fail + alert |

## 5. Manifest — `GET /v1/manifest`

JSON document validating against [`manifest.schema.json`](manifest.schema.json). It is the **entire** capability negotiation: Iris renders UI, validates jobs, estimates cost, and routes tasks purely from it. See the schema file for field-level docs; the shape follows TDD §3.4:

`spec_version, id, family, version, modality, tasks[], profiles{}, duration{}, resolutions{}, references{image|video|audio → {max, roles[]}}, conditioning{first_frame,last_frame,keyframes,depth_sequence,pose_sequence,mask,source_video,multi_view}, features{seed,negative_prompt,native_batch,lip_sync_in_gen,lip_sync_post,audio_gen,camera_control,v2v_restyle,video_inpaint}, params_schema{JSON Schema}, pricing{unit,per_profile_estimates}, limits{concurrency,max_queue}`

**Manifest honesty rule:** if it isn't declared, Iris won't send it and the UI won't show it. Under-declare when unsure.

## 6. Media I/O conventions

- All URLs (references in, artifacts out) are **short-lived signed HTTPS URLs**; endpoints MUST NOT require bucket credentials. Download refs at job start; treat expiry mid-job as `transient`.
- Reference images: PNG/JPEG/WebP, sRGB. Video: MP4/H.264 or ProRes. Audio: WAV/FLAC/MP3 ≤48kHz. Depth sequences: 16-bit PNG zip or grayscale MP4 (declared via `conditioning.depth_sequence.formats`).
- Artifacts: video MP4 (H.264 yuv420p baseline for drafts; profile may specify higher for masters), images PNG. Endpoint uploads via the presigned PUTs, then reports `uploaded: true` + `sha256`.

## 7. Open items (v1.0 freeze checklist)

- [ ] Streaming progress (SSE on the status endpoint) — nice-to-have, polling suffices at Phase 0 scale.
- [ ] Pose-sequence conditioning payload format (defer until R&D exposes it).
- [ ] Multi-view task shape (`views_requested[]` with camera params) — draft with R&D when multi-view serving is planned.
- [ ] Audio-conditioned generation params for Wan (in progress R&D-side) — reserve `references.audio.role: speech_lipsync` now (done) so no breaking change later.
