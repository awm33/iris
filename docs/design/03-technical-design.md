# Iris — Technical Design

**Status:** Draft v0.1 · July 2026
**Companion docs:** [High-Level Design](01-high-level-design.md) · [UI/UX Design](02-ui-ux-design.md)

Architecture for a web-first, desktop-ready, GPU-backed image/video editor. Opinionated defaults are stated with alternatives noted; anything marked **[decide]** is an open decision with a current lean.

---

## 1. System Overview

```
┌─────────────────────────── Browser (later: Tauri shell) ───────────────────────────┐
│  App shell (React)                                                                 │
│  ┌───────────────┐ ┌────────────────┐ ┌──────────────┐ ┌─────────────────────────┐ │
│  │ Image Studio  │ │ Video Studio   │ │ Story/Scenes │ │ Library / Jobs / Admin  │ │
│  │ canvas engine │ │ playback engine│ │  3D viewport │ │                         │ │
│  │ (WebGL2/GPU)  │ │ (WebCodecs)    │ │  (three.js)  │ │                         │ │
│  └───────┬───────┘ └───────┬────────┘ └──────┬───────┘ └────────────┬────────────┘ │
│          └────────────┬────┴─────────────────┴─────────────────────┘               │
│               Document runtime (op-based docs, undo, autosave)                     │
│               Media cache (OPFS/IndexedDB) · API client · WS client                │
└──────────────────────────────────┬──────────────────────────────────────────────────┘
                                   │ HTTPS (Connect-RPC/REST) + WebSocket (events)
┌──────────────────────────────────┴──────────────────────────────────────────────────┐
│                              API Gateway (authn/z, rate limits)                     │
│  ┌──────────┐ ┌──────────┐ ┌───────────────┐ ┌──────────────┐ ┌──────────────────┐ │
│  │ Core API │ │ Doc sync │ │ Generation    │ │ Media        │ │ Render/Export    │ │
│  │ (domain) │ │ service  │ │ orchestrator  │ │ pipeline     │ │ service          │ │
│  └────┬─────┘ └────┬─────┘ └──────┬────────┘ └──────┬───────┘ └────────┬─────────┘ │
│       │            │             │  model adapters  │ ffmpeg/GPU jobs  │ ffmpeg/GPU│
│  ┌────┴────────────┴───┐  ┌──────┴────────┐  ┌──────┴───────┐  ┌───────┴────────┐  │
│  │ Postgres (+pgvector)│  │ Iris GPU fleet │  │ Object store │  │ Job queues     │  │
│  │ domain + doc log    │  │ (Wan/Qwen srv) │  │ (S3) + CDN   │  │ (pg SKIP LOCKED│  │
│  │ + job queue/NOTIFY  │  │                │  │              │  │  + NOTIFY)     │  │
│  └─────────────────────┘  └───────────────┘  └──────────────┘  └────────────────┘  │
│                     ┌────────────────────────────────────┐                          │
│                     │ External model APIs (BYO keys via  │                          │
│                     │ server-side proxy + key vault)     │                          │
│                     └────────────────────────────────────┘                          │
└──────────────────────────────────────────────────────────────────────────────────────┘
```

**Hard boundary (lesson from the previous project):** Iris talks to *all* models — including our own — through one HTTP inference API + capability manifest. Model research code never lives in this repo; our Wan/Qwen-derived servers are a separate deployable owned by the R&D repo, versioned by manifest.

## 2. Frontend

### 2.1 Stack

| Concern | Choice | Notes |
|---|---|---|
| Language/framework | TypeScript + React 19 | Team-standard, ecosystem depth for panels/DnD. |
| Build | Vite + pnpm monorepo (Turborepo) | Packages: `app`, `canvas-engine`, `playback-engine`, `doc-runtime`, `ui` (design system), `api-client` (generated from protobuf — never hand-written) — plus `engine-core`, a Rust crate (wasm-bindgen → WASM for the browser packages; native build for the server renderer; see §2.3). |
| App state | Zustand (UI state) + doc runtime (document state) | Keep document state out of React state; React subscribes to doc-runtime selectors. |
| Data fetching | Connect-RPC web client + TanStack Query (`connect-query`) | End-to-end types generated from the protobuf schemas (see §3.1); additional interfaces added later if external consumers materialize — decided. |
| 3D viewport | three.js | Orbit, camera placement, depth-map render to texture. |
| Desktop-readiness | No direct DOM-only assumptions in engines; FS/media IO behind an interface | Tauri shell becomes a packaging task (Phase 2 decision). |

### 2.2 Canvas engine (Image Studio)

- **Rendering:** WebGL2 compositor (WebGPU backend when available, feature-flagged): tiled textures per layer, GPU blend modes, mask compositing, adjustment layers as shader passes. Target: 8k×8k documents, 60fps pan/zoom on integrated GPUs.
- **Document model:** layer tree with non-destructive ops; raster layers stored as tiles (256px) with per-tile dirty tracking; edits are ops (`brushStroke`, `setLayerProp`, `applyMask`…) appended to the doc log (see §4), rasterized into tiles locally.
- **Brush/paint:** GPU-stamped brushes; pressure via Pointer Events.
- **Selections:** vector paths + bitmap masks unified as a `Selection` object; AI subject-select calls a server segmentation endpoint (SAM-class model on the GPU fleet) returning a mask PNG.
- **Persistence:** tiles content-addressed and uploaded to object storage (deduped); canvas doc = op log + tile manifest. Export flattens server-side for large docs.
- **Color:** internal linear float16 pipeline, sRGB output at Phase 0; wide-gamut/HDR deferred.

### 2.3 Playback engine (Video Studio)

The hardest browser problem. Strategy: **frame-accurate compositing player on WebCodecs, fed by proxies, with server render as the escape hatch.**

- **Media prep (server, at ingest/take-creation):** every video asset gets (a) a **proxy** (H.264 720p, keyframe interval ~15, optimized for scrubbing), (b) filmstrip thumbnails, (c) audio waveform data, (d) probe metadata. Takes from our models are typically ≤1080p and short (seconds), so proxies are cheap and near-instant.
- **Playback:** timeline is compiled to a frame graph (clip → transform/effect/transition nodes). A scheduler decodes needed frames via WebCodecs `VideoDecoder` (proxy streams), composites in the same WebGL2 pipeline as the canvas engine (shared shader library — one effects implementation for both studios), and presents on a `requestVideoFrameCallback` clock. Audio via Web Audio API graph (per-clip gain/pan nodes, mixdown, ducking).
- **Scrubbing:** thumbnail strip for coarse, decoder-seek for fine; predecode a window around the playhead; drop-frame policy favors audio continuity.
- **Effects budget:** per-clip transform, opacity, LUT, color controls, dissolves are GPU-trivial. Anything beyond the real-time budget (heavy stacks, high-res) marks the region for **background preview render** (server renders that span to a proxy segment, cached by frame-graph hash — Premiere-style render bar, but server-side and automatic).
- **Rust/WASM engine core (preferred direction):** the performance-critical, DOM-free parts of the playback engine — frame-graph evaluation, decode scheduling, compositing math, waveform/color processing — are a strong fit for a **Rust core** (`wgpu`) compiled to **WASM + WebGPU** in the browser and **built natively for the server render service**. One codebase, two targets: this converts the client-preview ≡ server-export parity problem from a testing discipline into a structural guarantee, and aligns the frontend engines with the Go/Rust backend direction. Approach: define the engine-core API boundary (TS shell ↔ core) from day one; start hot paths in Rust/WASM where profiling justifies, rather than a big-bang rewrite. WebCodecs/`VideoDecoder` handles remain in the JS/TS shell (browser API), feeding frames to the core.
- **[decide]** Exact proxy ladder (single 720p vs adaptive) after dogfood telemetry; pace of moving compositing fully into the Rust core vs keeping the initial WebGL2/TS compositor.

### 2.4 Document runtime & collab-readiness

- All editable documents (canvas, timeline, story/scene structure) are **op-based**: an append-only log of typed operations + periodic snapshots. Client applies ops optimistically; the **doc sync service** is the single writer per doc (server-authoritative ordering), rebroadcasting acks over WS.
- Undo/redo = inverse ops on the local stack (op design requires every op to carry enough context to invert).
- **Why not CRDT now:** single-writer-per-doc at launch makes server-ordered op logs sufficient and much simpler; the op vocabulary is the hard part and is exactly what a later CRDT/OT layer needs anyway. This is the "collab without rewrite" insurance. **[decide]** Yjs adoption evaluated at Phase 3; op log migrates.
- Autosave = the log itself; named versions = tagged snapshots.

## 3. Backend

### 3.1 Stack

| Concern | Choice | Notes |
|---|---|---|
| Language | **Go and Rust primarily**: Go for services (core API backend, orchestrator, doc sync, media-pipeline coordination — fast to write, great concurrency); Rust where performance is the point (render/engine core, hot media paths). **Python only where it genuinely earns it for AI** — and prefer in-process inference via ONNX Runtime (or similar) from Go/Rust for light models (segmentation, embeddings) before reaching for a Python sidecar. Model servers live in the R&D repo regardless. | No TypeScript on the server — the frontend's typed contract comes from protobuf codegen (below). |
| API | **Connect-RPC (protobuf-first)**: `connect-go` servers; the browser talks Connect's HTTP protocol directly (no Envoy/grpc-web proxy), internal service-to-service uses the same definitions over gRPC. Protobuf schemas are the single source of truth → generated TS client + types for the React app. REST for webhooks/uploads. Additional interfaces (public REST via Connect's OpenAPI mapping, GraphQL) added later if needed — decided, not open. | Preserves tRPC's end-to-end typed DX with a Go backend; server streaming available for progress feeds if WS ever isn't enough. |
| DB | Postgres 16 + pgvector | Domain model, doc logs, job records, embeddings. One database until proven otherwise. |
| Queue/jobs | **Postgres-native queue: `FOR UPDATE SKIP LOCKED` claims + `LISTEN/NOTIFY` wakeups** (jobs are rows; workers poll-on-notify). Generation, media, and render jobs as separate queue tables/partitions with shared claim semantics. | One fewer moving part; jobs live in the same transaction as domain writes (job + take row commit atomically — a real correctness win for provenance). **[decide]** escalation trigger: sustained >~1k jobs/s or cross-service workflow complexity → dedicated queue (NATS) or Temporal. |
| Object storage | S3-compatible (R2/S3/MinIO for on-prem-future) + CDN | All media content-addressed: `sha256/<hash>` keys, metadata in Postgres. |
| Realtime | WebSocket service (job events, doc ops, presence-later) | Fanout via Postgres LISTEN/NOTIFY at launch scale (NOTIFY payloads are small — send keys, fetch bodies); swap to NATS/Redis pub/sub only if connection counts outgrow it. |
| Auth | **Open-source, self-hosted IdP for now** (Keycloak or a lighter peer — Zitadel/Authentik; pick at implementation) behind our own session layer | Workspace/member model from day one, single-user UX at launch. Managed IdP remains a later swap if ops burden warrants. |

### 3.2 Domain schema (core tables)

`workspaces, members, projects, scenes, sets, views, characters, shots, takes, sequences(edges), canvases, timelines, assets, asset_versions, asset_links(lineage edges), generation_jobs, model_endpoints, api_keys(vault refs), usage_events, doc_logs, doc_snapshots`

Key structural decisions:

- **`assets` are immutable versions**; mutable "asset identity" row points at head version. Every reference from a doc/shot/ref-chip records `(asset_id, version | 'head')` — explicit pin-vs-float per the HLD.
- **Lineage is a first-class edge table** `asset_links(from_asset_version, to_entity, role)` — roles: `generated_by(job)`, `reference_of`, `conditioning_frame_of`, `derived_from`, `used_in_take`, etc. The lineage graph UI and stale-propagation both read this table.
- **`sequences` stores continuity edges** `(prev_shot, next_shot, carry: jsonb)` where `carry` lists carried elements (last_frame, refs[], style, camera). Stale detection: trigger on take-selection change → mark downstream shots' `continuity_stale=true` via edge walk.
- **Takes** record complete provenance: `model_endpoint_id, manifest_version, prompt, params jsonb, ref_bundle jsonb, seed, parent_take_id, quality(draft|master), source(generated|imported)`.

### 3.3 Generation orchestrator

Job lifecycle: `draft → queued → dispatched → running(progress) → uploading → complete | failed | canceled`.

1. **Resolve:** expand the request's context — target, prompt template slots, ref chips → signed asset URLs, continuity payload (fetch predecessor's selected take → extract last frame *at request time*, not stale-cached), control inputs (depth sequences rendered by the media pipeline from 3D scene + camera path).
2. **Validate against manifest:** every input must map to a declared capability; unmappable inputs were already blocked in the UI, but the server re-validates (API consumers, races).
3. **Dispatch via adapter:** fan-out N takes = N inference calls (or native batch if the model supports it), each tracked as a sub-job. Adapters normalize: submit, poll/webhook progress, fetch artifacts, error taxonomy (safety-block vs transient vs invalid-input → different retry/UX).
4. **Land artifacts:** upload to object store → create asset versions + lineage edges + take rows → run media prep (proxy/thumbs/waveform) → emit WS events.
5. **Chains:** "regenerate chain" enqueues jobs with a `depends_on` column; a job becomes claimable only when its dependency reaches `complete` (enforced in the claim query), and each link re-resolves its continuity payload from the fresh predecessor result. Dependency release is a `NOTIFY` on the completing job's transaction commit — atomic with the take row it created.

**Draft/master:** draft = manifest-declared cheap profile (lower res/steps); master = full profile or upscale pass; both are takes linked by `derived_from`.

**Cost metering:** every job computes cost pre-flight (estimate; shown in UI) and post-flight (actual) into `usage_events` — internal GPU-seconds priced internally, commercial APIs from price tables, BYO-key jobs metered at $0 to us but still tracked for the user.

### 3.4 Model adapters & capability manifests

The manifest is the contract that keeps the UI honest and the churn contained:

```jsonc
{
  "id": "iris-video-2.2", "family": "wan2.2-iris", "version": "2026.06.1",
  "modality": "video",
  "profiles": { "draft": {"max_res": "832x480", "steps": 12}, "master": {"max_res": "1920x1080", "steps": 40} },
  "duration": { "min_s": 2, "max_s": 12, "extend": true },
  "references": {
    "image": { "max": 4, "roles": ["character", "style", "scene_view"] },
    "video": { "max": 1, "roles": ["motion"] },
    "audio": { "max": 1, "roles": ["speech_lipsync", "music"] }
  },
  "conditioning": { "first_frame": true, "last_frame": true, "first_and_last": true,
                    "depth_sequence": true, "pose_sequence": false, "multi_view": true },
  "features": { "lip_sync_in_gen": false,  // flips to true when R&D lands audio-conditioned Wan generation
                "lip_sync_post": true, "audio_gen": false, "camera_control": "depth_path",
                "v2v_restyle": true, "video_inpaint": "mask+prompt" },
  "params_schema": { /* JSON Schema for advanced panel */ },
  "pricing": { "unit": "gpu_second", "draft_est": 0.4, "master_est": 3.1 },
  "limits": { "concurrency": 8, "queue": "iris-gpu" }
}
```

Adapters (Phase 0/1): `iris-video`, `iris-image` (our fleet, internal HTTP), `openweight-generic` (user endpoints implementing our inference API or ComfyUI **[decide]** scope), `seedance`, `nano-banana`, **`elevenlabs`** (audio modality: TTS + voice cloning/design — the W4 dialogue workflow's voice source; music/SFX generation APIs later per demand), plus 2–3 more commercial APIs chosen from research findings. Commercial calls always proxy server-side (key custody, CORS, retries); BYO keys stored in a KMS-encrypted vault, decrypted only in the proxy path.

Audio is a first-class manifest modality (`modality: "audio"` — voices as model-level presets/params, per-character voice refs from the library). Note the sync capability split: `seedance` manifests declare audio-reference conditioning today; `iris-video` declares it when the R&D work to add audio-conditioned generation to the Wan stack lands (manifest-versioned, no UI change needed); post-hoc lip sync is a separate declared feature for models without it.

Research-informed adapter notes (July 2026; ✅ = adversarially verified, ◐ = primary-source unverified — see research doc):
- **Aggregator adapters are high-leverage:** one `fal` adapter yields many frontier models (✅: Veo 3.1 with synced audio, per-second pricing, t2v/i2v/extend/reference modes — no Google lock-in; use the `veo3.1` endpoint, `veo3` is deprecated). Consider `fal` (and/or Replicate) as the *first* commercial adapter, with first-party adapters added where pricing or exclusive features justify.
- **Do NOT build a Sora adapter:** the Sora API sunsets 2026-09-24 (✅; app already discontinued). Google Veo 3 & 2 Gemini endpoints shut down 2026-06-30 (◐) — Veo 3.1 only.
- **First-party pricing anchors for the cost-metering tables (◐):** Seedance 2.0 $0.35–3.89 per 5s by resolution (BytePlus, token-priced, video-input mode for continuation); Wan 2.7 API $0.10/s 720p, $0.15/s 1080p, silent = half price on i2v-flash; Veo 3.1 $0.05–0.60/s by tier/res; Nano Banana Pro $0.134/image (2K), Nano Banana $0.039; Seedream 5.0 charges +$0.003 per additional reference image (multi-ref is first-class in its API). Audio-in-generation ≈2× silent on both Seedance and Wan → **draft takes silent, audio on master**.
- **Manifest expressiveness check (from competitor APIs, ◐/✅):** support multi-keyframe conditioning (Pika interpolates 2–5 keyframes, not just first/last), per-segment prompts, extend-conditioned-on-final-*second* (Flow), and video-input continuation (Seedance 2–15s input). Notable: **Alibaba's newest Wan API generation (2.6/2.7) has no first/last-frame mode** — kf2v exists only on their 2.2/2.1 models. Our Wan-2.2 conditioning stack (first/last/depth) is not replicable via their API; a genuine self-hosted moat.
- **Open-weight roster licensing is not uniform:** Qwen-Image/Wan 2.2 are Apache 2.0 (✅), but FLUX.2 [dev]/klein require paid BFL self-hosting licenses for commercial volume (Builder/Platform/Professional tiers; separate Synthetic-Data license for using outputs as training data) (◐). The `openweight-generic` onboarding flow should record the license basis per endpoint; Lightricks LTX-2.3 (strongest downloadable video model, ✅) — check its license before rostering.
- **Qwen rebasing:** the Qwen-Image family is shipping consistency/multi-ref improvements in-family (Edit-2511, Image-2.0 native 2K); the R&D repo owns rebasing our custom capabilities onto newer bases, but manifests must version-carry these upgrades cleanly (manifest `version` + capability diffs, no UI hardcoding). The InstantX ControlNet inpainting adapter (Apache 2.0, diffusers/ComfyUI-native) is a starting point for `iris-image` inpaint/outpaint modes.
- **Wan 2.6/2.7 watch item:** weights release would give the fleet a major quality upgrade path; manifests isolate the swap. Also watch Runway GWM-1 (world-model API with multi-view + persistent scene memory, partial early access) — both a competitive clock on our 3D differentiation and a potential future adapter; and World Labs Marble's mesh/splat exports as an *import* path for Set 3D blockouts.

### 3.5 Media pipeline

Workers (Go/Rust, containerized ffmpeg + GPU where useful) fed by the pg queue: probe → transcode proxy → thumbnails/filmstrips → waveform JSON → (video takes) last-frame + first-frame extraction as image assets (pre-materialized because continuity uses them constantly) → embeddings (CLIP-class) into pgvector for semantic search. Light inference (embeddings, SAM-class segmentation for AI subject-select) runs **in-process via ONNX Runtime** in these workers where practical, reserving the GPU fleet and Python for the generative models proper. Depth-sequence rendering from 3D scenes also lives here (headless renderer container).

### 3.6 Render/export service

Server render must be **deterministic with client preview**: it consumes the same compiled frame graph JSON and runs the **same Rust engine core natively** (`wgpu` on GPU nodes) that the browser runs via WASM — one implementation, two build targets — sourcing original-quality media instead of proxies, muxing via ffmpeg. (Fallback if the Rust core lags the TS compositor early on: headless-chromium rendering of the shared WebGL pipeline as a stopgap, retired once the core reaches parity.) Deliverables: export presets (H.264/HEVC socials, ProRes master), caption burn-in or sidecar, still exports from canvases. Also serves the background preview-render path (§2.3).

### 3.7 Multi-tenancy, security, ops

- Workspace-scoped rows + Postgres RLS as defense-in-depth behind the API's authz.
- Signed, expiring media URLs (CDN-signed); no public buckets. Uploads via presigned multipart with server-side probe/AV scan before activation.
- Prompt/media safety: inherit model-side safety for commercial APIs; our models get a policy filter service at the adapter layer **[decide: scope for internal alpha vs beta]**.
- Observability: OpenTelemetry traces end-to-end (a generate click → adapter → GPU fleet → artifact land), job-queue dashboards, GPU utilization/cost dashboards; error budget on job-failure rate.
- Deploy: containers on k8s (API/workers autoscale on queue depth); GPU fleet capacity separate; IaC from day one. Single region at launch.

## 4. Data-flow walkthroughs (signature workflows)

**W3 shot generation:** UI Generate(shot 3) → `core-api.jobs.create` (resolve refs: Mara turnaround v4, Diner/counter view v2; continuity: shot 2 selected take → pre-materialized last-frame asset; depth chip: cam V2 path → depth sequence job dependency) → orchestrator validates vs `iris-video-2.2` manifest → 4 sub-jobs to GPU fleet → artifacts land as takes (draft) with lineage edges → WS events → take picker populates → user commits T3 → master render job → timeline clip re-resolves to master take → shots 4–5 flagged stale via sequence edges.

**W6 frame fix:** take frame → `Edit in Image Studio` (server extracts full-res frame → new canvas with frame as base layer) → gen-fill ops → `Promote → Video keyframe` (canvas flatten export → asset version + lineage `derived_from` take frame) → generate panel pre-fills video-inpaint or first-frame-conditioned regeneration depending on selected model's manifest.

## 5. Phase 0 build order (engineering sequence)

1. Monorepo, auth, workspace/project CRUD, object storage + upload/ingest pipeline, asset model with versions/lineage.
2. Generation orchestrator + iris adapters + manifests + jobs UI/tray (thin UI shell around it). *At this point the team can already generate with refs — dogfooding value starts.*
3. Doc runtime + Story/Scene/Set/Character surfaces (the domain UI).
4. Image studio: canvas engine core (layers/brush/selections/transform) → gen-fill loop → promote-to-asset.
5. Video studio: media prep + playback engine (proxy playback, trim, tracks, audio basics) → shots/takes/placeholder clips → take picker → continuity chains.
6. Export service; captions; color basics.

Rationale: the orchestrator-first order means every subsequent surface plugs into working generation, and internal users get value from week ~4, not month ~6.

## 6. Key risks & alternatives

| Risk | Position |
|---|---|
| WebCodecs playback engine complexity | Highest-risk component. Mitigations: proxies keep decode cheap; server preview-render escape hatch; ship trim-accuracy before effects-depth. Alternative considered: server-side playback streaming (Bitmovin-style) — rejected for latency/cost of interactive scrubbing. |
| Shared render pipeline (client preview ≡ server export) drift | Structural fix: one Rust engine core built for WASM (browser) and native (server). Golden-frame perceptual-diff suite in CI regardless — GPU drivers and codecs still vary across targets. |
| Rust/WASM core slows early iteration (small team, two languages in the frontend) | Engine-core API boundary defined day one, but Phase 0 may ship the TS/WebGL2 compositor first and migrate hot paths incrementally; the boundary is the commitment, the rewrite pace is data-driven. |
| One DB bottleneck (now also carrying the job queue) | Fine at this scale; queue tables are append-heavy — partition + aggressive vacuum/archival of terminal jobs; doc logs partition by doc, snapshots archive to object store. Queue escalation path (NATS/Temporal) documented; job records are engine-agnostic. |
| Tauri later reveals web-only assumptions | Engines take an IO interface from day one; CI builds a headless engine target to keep the seam honest. Rust core helps here too — it compiles into a Tauri shell natively. |

## 7. Open technical decisions

*(Updated July 2026 — items 1–2 decided.)*

1. ~~tRPC vs GraphQL~~ — **decided:** Connect-RPC (protobuf-first, `connect-go` + generated TS client) — gRPC-compatible internally, browser-direct externally; additional interfaces (public REST/GraphQL) added later if needed.
2. ~~Open-weight endpoint contract~~ — **decided:** our inference API spec for now; ComfyUI import revisited on demand signal.
3. Rust engine-core rollout pace: which hot paths move from the initial TS/WebGL2 compositor into the Rust/WASM core first, and when the server renderer drops the headless-chromium stopgap (if it was ever needed).
4. Which open-source IdP (Keycloak vs a lighter peer — Zitadel/Authentik); pick at implementation.
5. Postgres-queue escalation threshold and target (NATS vs Temporal) — documented trigger: sustained ~1k jobs/s or multi-service workflow complexity.
6. Region/GPU capacity plan for beta (depends on fan-out telemetry from dogfooding).
