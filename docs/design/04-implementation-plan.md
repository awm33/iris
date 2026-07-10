# Iris — Implementation Plan

**Status:** v0.2 · updated 2026-07-08 (see §0)
**Companion docs:** [High-Level Design](01-high-level-design.md) · [UI/UX](02-ui-ux-design.md) · [Technical Design](03-technical-design.md) · [Research](../research/2026-07-market-and-model-landscape.md)

---

## 0. Current state (as of 2026-07-08)

**M0 is functionally complete.** Everything below is implemented AND verified by execution on the dev machine, not just written:

- ✅ Monorepo scaffold (`proto/`, `spec/`, `backend/`, `engine/`, `web/`, `deploy/`), git initialized (initial commit pending user go-ahead).
- ✅ Dev stack runs: Postgres+pgvector, MinIO, dockerized mock models. **Ports:** Postgres host **15432**, MinIO **9100/9101**, API **8280**, mocks **8900** (video) / **8901** (image) — 5432/8080/9000 are taken by other local stacks.
- ✅ Migration `0001` applies cleanly (full core schema: assets/versions/lineage, story domain, jobs queue columns, doc-op tables).
- ✅ `buf lint` + codegen produce Go (connect-go servers) and TS clients; backend builds with generated code (deps: connect v1.20, protobuf v1.36).
- ✅ `pnpm install` + typecheck green; Rust `engine-core` checks + tests green (native; wasm32 target in CI config).
- ✅ **Inference API spec + manifest JSON Schema** (`spec/`) — the R&D contract, v1.0-draft.
- ✅ **Mock model server** (`backend/internal/mockmodel` + thin cmd): full contract incl. **real artifact upload** to presigned PUTs (procedural PNG for image modality; embedded 2s canned MP4 for video), idempotent create, cancel-responsive, failure injection (`FAIL:safety`, `FAIL:transient` keyed by prompt so orchestrator-shaped retries succeed, `SLOW`).
- ✅ **Conformance suite** (`backend/internal/conformance` + `cmd/conformance` CLI): 8 checks — health, auth-required, manifest-validates-against-schema (embedded copy with drift-guard test), job lifecycle with artifact sha256 verification, idempotency, cancel, safety_blocked taxonomy, transient-retryable taxonomy. Mock passes 8/8 in CI (`go test`) and via the CLI. `just conformance <url>` for R&D.
- Toolchain: Go 1.25, buf/goose/just installed via brew, pnpm 9 via npm.

**Contracts stance (decided 2026-07-08):** contracts are adopted as **working drafts** — reviewed, good enough to build on, expected to evolve as wiring surfaces reality. Guards are mechanical, not process: `buf breaking` in CI, manifest `spec_version`, migration discipline. R&D handoff (`spec/` + `cmd/conformance`) happens when R&D is ready to build; no ceremony before that.

**Environment stance (decided 2026-07-08):** all development runs against **local docker + mocked model endpoints** (`mock-model` video/image). No real remote endpoints — R&D or commercial — are wired until dogfood checkpoints call for them. Commercial adapters (M6) start against recorded/mocked API shapes.

**Repo & process (2026-07-08):** remote is `github.com/awm33/iris` (may move under bucreative later). Work lands via **commits and PRs; each PR gets independent subagent reviews** before merge. GitHub Actions/CI is **deliberately parked** (workflow kept inert at `.github/workflows-disabled/`) — `just check` is the local gate until CI is switched on.

**M1 progress (2026-07-08, same session):** the walking skeleton is live and browser-verified —
- ✅ Auth v0: seeded dev workspace (`ws_dev`) attached to every request (no IdP; per environment stance).
- ✅ `WorkspaceService` + `AssetService` implemented (connect-go, pgx store — hand-written queries, sqlc when the surface stabilizes) with migration `0002` (pending_uploads).
- ✅ Upload path: presigned single-part PUT → sha256 **content-addressed promote** (`sha256/<hash>`, dedup free) → asset + version rows; image dims via stdlib decode. Verified: byte-identical download roundtrip; video upload maps kind correctly (probe/duration is media-worker's job — M2).
- ✅ Frontend shell: left-rail IA (future rails visible but disabled), Projects page (list/create), Library page (upload button → full presigned flow from the browser incl. MinIO CORS, image thumbnails via `SignDownload`). Screenshot: `.dev/m1-library.png`.
- ✅ `GetLineage` API exists (empty until M2 writes edges).
- Servers for local dev: `just api` (:8280) + `just web` (:5173); stack via `just dev`.

**M1 — ✅ COMPLETE** (PR #1, merged `df4f13f`): media-worker ingest probe — ffprobe metadata via signed URLs, video posters, first pg-queue consumer with the lease/reaper/ownership design that survived independent review (stranded-`running` recovery, ffmpeg protocol whitelist, per-job panic recovery). Filmstrips deferred to M5 with the rest of media prep.

**M2 progress (PR #2, merged `0ddc919`) — backend core ✅ COMPLETE:**
- Inference client + parsed capability manifests with two-stage validation (click-time + dispatch-time), JSON-Schema-gated endpoint health, last-known-good manifests.
- Endpoint registry (dev-seeded mocks, refresh loop, on-demand refresh).
- Generation queue: lease/heartbeat/reaper, `depends_on` gating **with transitive dependency-failure propagation**, parent rollup (lock-then-aggregate; race-proof), error taxonomy, unreachable-endpoint requeues that don't burn attempts.
- Orchestrator: resolve refs (pin-or-head) → dispatch (per-attempt endpoint ids + upload keys) → poll with heartbeat-as-cancel-detection → atomic artifact landing (content-addressed, sha-verified, lineage edges, probe enqueue for image+video, usage metering).
- GenerationService: CreateJob (fan-out ≤8, seed derivation), Get/List/Cancel(idempotent)/Retry(guarded), ListModelEndpoints.
- **Endpoint responses treated as untrusted** (own content types, probe-measured metadata, clamped metering) per security review; hardening backlog below.
- Two independent review rounds; findings incl. a critical dependency-deadlock fixed and live-verified.

**M2 — ✅ COMPLETE** (PR #3, merged `891bb6f`): SSE event bridge (coalesced per-client dirty flags — lossless "something changed" semantics, resync on reconnect, poll backstop; SSE is an accelerator, never load-bearing), generate panel v1 (manifest-driven), Jobs page with live cancel/retry and artifact posters. Browser-verified incl. the lost-terminal-event recovery scenario.

**M3 progress (PR #4) — story domain backend:**
- `story.proto` + StoryService: Scenes (auto-owning Sets), Views (promote-to-View from any library image), Characters (ref bundles, atomic jsonb append), Shots, Takes (list/select).
- Orchestrator lands shot-targeted generations as **Takes** in the artifact transaction (recipe = endpoint/model/task/profile/request provenance; `used_in_take` lineage; first take auto-selects, selection revisitable). CreateJob validates shot targets.
- **Deviation noted:** story structure is relational CRUD, not op-log docs — the doc runtime debuts with the canvas (M4) where it's unavoidable; collab-readiness for story rows revisits with Phase 3.
- Curl-verified W1/W2/W3-image: scene → promote view → cast character → shot-targeted fan-out → takes with provenance → re-select.

**M3 — ✅ COMPLETE** (PRs #5 `c94662e`, #6 `b284104`): the story UI — Scenes/Scene pages (views strip, cast, shots with generating badges), Character pages (ref bundles with role-kind enforcement), promote-to-View/Character from the Library, **Take Picker** (grid, keyboard contract, selected state) and take iteration (♻ regenerate-from-this with recipe pre-fill incl. raw-JSON seed extraction). Browser-verified end-to-end: the W1/W2 image-side exit moment works in the UI. Deferred from the M3 list: `@`-mentions in the prompt (the generate panel's reference chips cover the need for now; revisit with canvas/timeline mention surfaces), hover-scrub (with M5 filmstrips).

**Stock sources (PR #7 `f6bb5ff`, unplanned addition):** Pexels stock-photo import — search modal in the Library, imports land as normal content-addressed assets with same-transaction attribution meta and idempotent re-import (provenance dedup). `internal/sources/` is deliberately the slot where commercial generator adapters land later. Hardened per review: resolved-URL validation (https + `*.pexels.com`, per-redirect), truncation-detected size cap, bounded memory, accurate error codes. Key via gitignored `.env` (`IRIS_PEXELS_API_KEY`, see `.env.example`); rationale: real reference plates for dogfooding M4 before generation quality/commercial adapters arrive.

**M4 slice 1 — ✅ COMPLETE** (PRs #8 `2bc7f54`, #9 `6de5dc0`):
- **Canvas foundation (PR 8):** doc runtime v1 (op-log, undo-as-op, autosave with conflict rebase + lost-ack dedup, keepalive unload flush), CanvasService (head_seq append lock, gapless seqs, ABORTED contract), canvas page (pan/zoom, layers, brush/eraser with live-stroke rebuild hook, open-image-as-canvas, flatten-export). Canvas2D behind a compositor seam. 20 doc-runtime tests.
- **Gen-fill loop (PR 9, the W6 exit):** marquee/lasso → inpaint via the generation pipeline (contract gained `conditioning.source_image`; mask semantics pinned: white = generate) → in-place candidate strip → commit as masked layer (`mask_version_id` in the op vocabulary; drift-proof: outside-mask pixels always come from layers below) → promote-canvas-to-View. Random fan-out seeds now resolve concrete per-sub-job (recipes reproducible — closed that backlog item) gated on `manifest.features.seed`. Conformance grew a `mask_semantics` check (9 checks total); conditioning inputs get lineage edges. Browser-verified end to end.

**M4 slice 2 (started 2026-07-09):** per the remove-tool strategy above —
- **PR 10 — Remove tool UX (mock-first):** empty-prompt inpaint = removal (spec documents prompt as optional for inpaint); ✂ Remove beside Generate on an active selection, single candidate, same commit flow. Mock parodies content-aware fill (surrounding-ring average) so removal is visually meaningful against mocks.
- **PR 11 — LaMa endpoint trial:** a small dockerized endpoint (ONNX runtime) implementing spec/inference-api.md tasks:["inpaint"] — drop-in by manifest negotiation, zero UI change, conformance-tested incl. mask_semantics. First real local model; consistent with the environment stance (local, not a remote).
- Then: SAM subject select (click-to-mask feeding remove/gen-fill), text layer v0.

**M4 — ✅ COMPLETE** (PRs #10 remove tool, #11 LaMa endpoint, #12 SAM subject select, #13 text layer — each independently reviewed, all findings fixed). The image studio ships: canvas/layers/brush/eraser, gen-fill with in-place candidates, the full Remove ladder (SAM click-to-mask → LaMa fast tier → removal-tuned Qwen-Edit as the standing R&D quality-tier ask, with dogfood eval cases recorded), promote-to-View, text v0. Notable infra: first two REAL local models (LaMa, MobileSAM) as sha256-pinned dockerized services; conformance suite is manifest-aware with mask_semantics + removal checks; `features.prompt` capability flag.

**M5 slicing (started 2026-07-09):**
- **PR 14 — media prep ✅:** probe chains a `prep` job — 720p H.264 proxy, filmstrip strip (~50 tiles), first/last full-res frames (the carry-last-frame input, HLD W3), waveform peaks JSON. Keys in version meta; SignDownload variants.
- **PR 15 — clip player ✅:** single-clip playback on native `<video>` over the proxy (recorded deviation: WebCodecs debuts with the timeline compositor, where per-frame multi-clip control is unavoidable), J/K/L + frame-step + filmstrip/waveform scrub.
- **PR 16 — timeline doc + shots as clips:** timeline op vocabulary on the shared doc runtime, placeholder clips at target duration, generate-into-slot; selected-take resolution in preview is a recorded TODO. Review deferral: **genericize `activeOps`/the doc shell in doc-runtime before a third doc kind** — TimelineDoc currently duplicates the CanvasDoc shell behind `as unknown as` casts; a third copy is the trigger to extract the generic base.
- **PR 17 — timeline edit tools ✅:** trim handles (in-point slip), blade at playhead, edge/playhead snapping, selection + delete.
- **PR 18 — timeline playback v1 ✅:** wall-clock rAF playhead with chasing preview, selected-take resolution via GetShot (shot slots play their takes).
- **PR 19 — Story board page:** scene columns, shot cards w/ take thumbs + state badges, drag reorder, inline add scene/shot; board is the project landing. Deferred from spec: continuity-chain arrows (land with carry-last-frame), "Open in Timeline" (timeline's shot picker covers assembly).
- **PR 20 — continuity carry ✅:** first_frame conditioning end-to-end — a VIDEO version ref means "its last frame" (prep artifact, resolved at dispatch); GeneratePanel ⛓ chip carries the nearest upstream selected take; SelectTake changes mark downstream shots-with-takes ⚠ stale, fresh landings clear it. Unblocks the board's chain arrows.
- **WebCodecs compositor slicing (started 2026-07-09):**
  - **PR 22 — media-engine package:** `@iris/media-engine` — mp4box demux + VideoDecoder pipeline behind a small surface (`ClipDecoder.open(url)` → `frames(fromS)` async generator from the nearest sync sample). Pure logic (sample index/seek-point math, frame-queue eviction) split from WebCodecs so it unit-tests in vitest; live testbed = an engine mode in ClipPlayer painting to canvas with decode stats.
  - **PR 23 — timeline compositor v1:** canvas PreviewPane driven by the engine — gapless clip boundaries via next-clip prebuffer, placeholder cards composited, graceful fallback to the <video> chase where WebCodecs is unavailable.
  - **PR 24 — audio mixing:** decodeAudioData on proxies (prep already encodes AAC), static scheduling on the AudioContext clock (audioSchedule pure + tested), video-segment embedded audio + audio-track clips, overlaps mix (NLE semantics). Engine preview becomes the DEFAULT (⚙ toggles back to <video>). Recorded deviations: wall clock still drives the playhead (audio anchored at play start — drift inaudible at dogfood lengths; audio-clock-driven playhead when timelines grow), audio schedules once per play (mid-play edits reschedule on next play), per-clip gain lands with mixer UI.
- **PR 25 — take-aware source binding ✅:** Shot.selected_take_content_type resolved server-side; image takes render as stills (engine overlay + PreviewPane <img>), are excluded from decode segments, and carry instantly (no last_frame prep needed); resolved shot clips blade/trim with source continuity (bladeOps hasSource).
- **PR 26 — ripple trim + delete ✅:** rippleOps (pure) + ⇧ right-edge trim / ⇧ delete gestures. Recorded deviations: same-track only (no sync-lock — rippling V1 leaves A1 behind; multi-track ripple lands with track targeting), no left-edge ripple (conventions diverge), no live drag preview (later clips jump on release), N+1 undos per ripple (op grouping is the standing vocabulary ask).
- **M5 COMPLETE.**

**M6 slicing (started 2026-07-09):**
- **PR 27 — regenerate-chain + pin/freeze:** carry_from_depends_on (dispatch-time resolution from the dependency's landed artifact, provenance written back into the request so recipes/chains/lineage stay true), RegenerateChain RPC (recipe replay per chained shot, depends_on ordering, stops at pinned/unchained), pinned shots skipped by stale propagation, board 📌 toggle + inspector ⛓⟳ CTA. Completes the W3 exit criterion.
- **PR 28 — generation response cache ✅:** identical resolved requests replay landed artifacts (dev/test helper, all adapters; IRIS_GEN_CACHE, dev-default on).
- **PR 29 — adapter seam + seedance + vault:** adapters package (one client surface for registry + orchestrator; kind routes), vault (auth_ref → secret, resolved ONLY at client construction; env: refs in dev, KMS later), seedance adapter (recorded Ark v3 shapes → our spec: static manifest, text-command params, first_frame image role, result-URL download → presigned re-upload proxy path, error taxonomy incl. safety_blocked), mock-seedance server + dev endpoint seed. In-process adapters presign INTERNAL blob URLs. NOTE: recorded shapes need verification against the live API before real keys. TICKETED before real keys: persist the adapter's remote task id on the job row and re-attach on reclaim instead of re-submitting (restart/retry currently re-submits = duplicate paid generation, old task never canceled); strip `--` flags from user prompts (text-command injection/cost control); per-workspace vault rows replace process-env refs.
- **Next:** elevenlabs adapter (audio modality: artifact handling, probe/prep for audio takes), audio refs in the panel, post-hoc lip-sync integration point. Backlog: transactional `ReorderShots(scene_id, ordered_ids)` RPC — the board renumbers client-side today (N sequential UpdateShot calls, non-atomic on partial failure, self-healing on the next reorder).

**Remove tool strategy (decided 2026-07-08):** removal is served by the existing `inpaint` task with **pluggable backends** — the manifest already negotiates capability, so no contract change. Rationale: removal is a distinct task from generative inpainting (instruction editors have a re-insertion bias; Photoshop's Remove beat Nano Banana Pro at pure removal in our testing), but our masked-layer commit neutralizes whole-image drift by construction, so the gap is in-mask only. Plan: trial **LaMa-class ONNX** on the media worker (instant, unmetered — same runner as SAM subject-select, slice 2) as the fast tier; **Qwen-Image-Edit** as the quality tier — R&D's automated person-from-background removal (training-data pipeline) already validated it, and that same pipeline produces the counterfactual pairs (with/without object) a **removal-specialized Qwen-Edit fine-tune** would train on (RORem/ObjectClear recipe) if/when the stock editor's re-insertion bias or shadow handling warrants it. Route by mask size (Photoshop Mode:Auto pattern), manual override. Commercial APIs are for gen-fill insertion/replacement, not removal.

---

**Planning assumptions (confirmed):** 1–2 engineers + Claude (agentic coding is a first-class team member — milestones assume heavy generation of well-specified code against frozen contracts). The R&D team has **no stable serving API today**: Iris defines the inference API + capability manifest spec (this repo, `spec/`), R&D implements it, and Iris develops against a **mock model server** until real endpoints land.

---

## 1. How we work (rules that shape everything below)

1. **Thin vertical slices over horizontal layers.** Every milestone ends with something a human uses end-to-end in the browser, however narrow. No milestone is "the backend for a future milestone."
2. **Contracts first, frozen early.** Four contracts get authored and reviewed before the code that depends on them: the **protobuf API** (`proto/iris/v1`), the **inference API spec** (`spec/inference-api.md`) + **manifest schema** (`spec/manifest.schema.json`), the **DB schema** (migrations), and later the **doc-op vocabulary** (M3) and **frame-graph JSON** (M5). With 1–2 people + Claude, stable contracts are what make generated code composable.
3. **Mock-first model integration.** `cmd/mock-model` implements the inference spec fully (manifest, jobs, progress, fan-out, artifacts — canned/procedural media, simulated latency and failures). All of Iris is built against it; swapping in the real R&D endpoint is a config change. The mock doubles as the **conformance suite** R&D tests against.
4. **TS/WebGL first where the Rust core would slow the first slice.** The `engine-core` Rust crate exists from day 0 and owns the *API boundary*, but M4/M5 may ship compositing in TS/WebGL2; hot paths migrate into Rust/WASM when profiling — not ideology — says so (per TDD §2.3/§6).
5. **Dogfood checkpoints are the schedule.** Each milestone's exit criterion is a signature-workflow moment (W1–W6), not a feature list. If the team can't do the workflow moment, the milestone isn't done.
6. **Defer by default.** Anything not on a milestone's critical path goes to the backlog section, not into the milestone.

## 2. Milestones

Sized in relative units (one unit ≈ one focused week for 1 engineer + Claude; calendar mapping depends on who's staffed). Serial by default; ⇄ marks work that can overlap if a second engineer exists.

### M0 — Foundations & contracts — ~1 unit — ✅ DONE (2026-07-08, see §0; pending human contract review)
Repo scaffold (monorepo: `proto/`, `spec/`, `backend/`, `engine/`, `web/`, `deploy/`), dev environment (`docker-compose`: Postgres+pgvector, MinIO, mock-model), CI stub (buf lint/generate, go vet/test, cargo check, pnpm typecheck), **contracts v0**: initial protos (workspace/project/asset/job services), inference API spec + manifest JSON schema, initial DB migration (core tables from TDD §3.2). *Exceeded scope: mock-model uploads real artifacts; conformance suite shipped (was an M0-exit deliverable, now done).*
**Exit:** `just dev` brings up the stack ✅; `buf generate` produces Go server + TS client stubs ✅; mock-model serves a manifest and completes a fake job ✅ (with verified artifact upload). **Spec handed to R&D** ← the one open item.

### M1 — Walking skeleton — ~2 units
Auth v0 (session cookies against a single seeded workspace/user; OSS IdP wired later — don't block on Keycloak), workspace/project CRUD through the full stack (Go Connect service → generated TS client → React shell with the left-rail IA), object storage upload/ingest (presigned multipart → probe → asset + asset_version rows), library page listing assets, lineage edge table in place (even if the UI is a stub).
**Exit:** create a project in the browser, upload an image and a video, see them in the Library with versioned asset rows behind them.

### M2 — Generation core (the value unlock) — ~3 units
Postgres queue (SKIP LOCKED claim + NOTIFY release), orchestrator service (resolve → validate-vs-manifest → dispatch → land artifacts with lineage), `iris-video`/`iris-image` adapters speaking the inference spec to mock-model, fan-out N sub-jobs, draft/master profiles, jobs page + queue tray, WS events (job progress → browser), generate panel v1 (prompt, ref chips from library, model picker off manifests, count).
**Exit (dogfood moment):** type a prompt with a reference image from the library, get 4 candidates, pick one, see it land in the Library with full provenance — against mock-model. **When R&D's endpoint passes the conformance suite, the same flow runs on real Wan/Qwen** — the plan does not block on that date.
⇄ R&D implements the spec in parallel from M0's handoff.

### M3 — Domain surfaces (the product's nouns) — ~3 units
Doc runtime v1 (op-log documents, server-ordered, autosave, undo) for *structural* docs first (story/scene, not canvas yet), Scene/Set/View/Character/Shot/Take schema + services, Story board page (scene columns, shot cards, state badges), Scene page (views strip, cast), Character pages, promote-to-View/Character flows from Library, `@`-mention references in the generate panel, **Take Picker v1** (grid, hover-scrub for images, commit/selected state).
**Exit (W1/W2 image-side):** create Scene "Diner" → generate a plate → promote to View → cast a Character with refs → generate a shot's *image* keyframes citing `@Mara` + `@Diner/wide` → pick a candidate in the Take Picker.

### M4 — Image studio slice 1 — ~4 units
Canvas engine v1 (tiled raster layers, WebGL2 compositing, pan/zoom, layers panel, brush/eraser, marquee/lasso selection, move/transform, undo via doc ops), **gen-fill loop** (selection → generate panel targets it → candidate strip renders in place → commit as masked layer with provenance badge), text layer v0, promote-canvas-to-asset, AI subject select (SAM-class via ONNX in a media worker).
**Exit (W6 + W1 refinement):** open a generated plate, select a region, gen-fill it with 4 candidates, arrow through them in place, commit, promote the result to a View.
*(Adjustment layers, clone/heal, filters, multi-view expansion → M6/backlog.)*

### M5 — Video studio slice 1 — ~4 units
Media prep pipeline (proxy transcode, filmstrips, waveforms, first/last-frame extraction — Go workers + ffmpeg off the pg queue), playback engine v1 (WebCodecs proxy playback, J/K/L + scrub, 2 video + 2 audio tracks, blade/ripple trim, clip gain, snapping), **shots as timeline citizens** (placeholder clips at target duration, generate-into-slot, selected-take resolution, takes flyout → Take Picker with synced video playback), timeline doc ops (shared runtime with M3).
**Exit (W3 core):** write Shots 1–3 as placeholders, generate 4 takes each (mock or real), pick takes, trim the assembled scene, play it smoothly.
**Engine-core checkpoint:** profile playback; decide which paths (frame scheduling, compositing) move into Rust/WASM in M7 vs stay TS.

### M6 — Continuity, audio & the AI-video loop — ~3 units
Sequence edges + carry payloads (last frame, refs, style), "generate next shot from this one" + chain inspector, stale propagation + pin/freeze, regenerate-chain (queued with `depends_on`), **`elevenlabs` adapter** (TTS/voice, character voice refs, generate-voice-in-panel), **`seedance` adapter** (first commercial adapter — pulled forward from Phase 1 because it's the only in-gen lip-sync path until R&D's audio-conditioned Wan lands; also proves the BYO-key vault + proxy path), audio refs in the generate panel, post-hoc lip sync integration point (manifest-declared).
**Exit (W3 full + W4):** 3-shot chain where each shot continues from the predecessor's selected take; re-pick shot 1 → downstream flagged stale → regenerate chain. Dialogue shot: ElevenLabs voice → audio-ref generation via Seedance (or post-hoc fallback).

### M7 — Finish & first full dogfood — ~3 units
Export service v1 (server render of the frame graph — TS-shared-shader or early Rust-native per the M5 checkpoint; H.264 presets + captions burn-in), auto-transcribe → caption track (+ text edit), color basics (LUT + exposure/contrast/temp per clip), transitions (cut/dissolve), audio ducking, golden-frame CI (client preview vs server export perceptual diff), 3D set import + camera → depth-sequence render path (the R&D-differentiating control input) **if R&D endpoint supports depth conditioning by now; else stays behind mock**.
**Exit (W5 — THE dogfood):** the team produces a complete short multi-scene piece (≥1 min) entirely in Iris — script beats → sets/characters → generated takes → cut → captions → color → export. Phase 0 (HLD §7) is done.

### Security-hardening backlog (from PR 1–2 independent reviews; prerequisites for auth-v1 / BYO-endpoints, not blockers today)
- Workspace scoping on ALL read/mutate paths (SignDownload, GetLineage, generation Get/List/Cancel/Retry, reference resolution in the orchestrator) — the IDOR class once multi-user auth exists.
- SSRF egress controls before BYO endpoint URLs: https-only, dialer deny-list for RFC1918/link-local/metadata ranges, redirect caps.
- Promoted-blob GC sweep (landing rollbacks / sha-mismatch orphans under `sha256/`; stale `uploads/` temp keys).
- Artifact upload size ceiling (presigned PUT content-length range or post-hoc check).
- Cost model cleanup: `cost_estimate` (manifest pricing units) vs `cost_actual` (gpu-seconds) aren't comparable; unify when billing becomes real.
- Endpoint auth tokens to KMS/vault (auth_ref is plaintext in dev).
- Event bridge (PR 3 review): workspace-scoped event filtering needs workspace id in NOTIFY payloads — decide the payload shape before more pg_notify call sites accrete (8 today); SSE per-write deadlines; connection sharing across tabs (BroadcastChannel/HTTP2); kind-aware artifact thumbnails in ListJobs (avoids poster-404 probing for images).

### M8+ — Phase 1 backlog (not planned in detail yet)
Additional commercial adapters (`fal` first — one adapter, many models), nano-banana; capability-manifest UI polish; multi-view expansion; adjustment layers/clone/heal/filters; speed ramps; shot-match color; onboarding/Assist mode; project export; OSS IdP (Keycloak-class) replacing auth v0; review links. Re-plan after the M7 dogfood retro.

## 3. The R&D dependency track

| When | Handoff | Direction |
|---|---|---|
| M0 exit | `spec/inference-api.md` + `spec/manifest.schema.json` v1.0 (frozen; changes via versioned RFC) | Iris → R&D |
| M0 exit | Conformance suite = mock-model's test harness (`backend/cmd/mock-model` + `backend/internal/conformance`) | Iris → R&D |
| M2+ | Real `iris-image` endpoint (Qwen-derived) passing conformance — images first, simpler | R&D → Iris |
| M5± | Real `iris-video` endpoint (Wan-derived): t2v/i2v, first/last-frame conditioning, image refs | R&D → Iris |
| M6–M7 | Depth-sequence conditioning; audio-conditioned generation (in progress per July decision); multi-view | R&D → Iris |

Iris never blocks on this track — every capability has a mock. But each real-endpoint arrival triggers a dogfood checkpoint against real quality.

## 4. Week 1 (concrete)

1. Review + freeze contracts v0: protos, inference spec, manifest schema, migration 0001 (this scaffold produces the drafts — the human pass is the week-1 job).
2. Walk the spec with the R&D team; adjust once; tag v1.0.
3. `just dev` green on both engineers' machines; CI green on the scaffold.
4. Start M1: auth v0 + workspace/project service (backend) ⇄ app shell + left-rail IA (frontend).
5. Stand up the shared dogfood instance target (where it deploys, even if manually) — deploy pain surfaces early.

## 5. Engineering conventions (defaults, change with evidence)

- **Go:** 1.22+; `connect-go` services; `sqlc` for typed queries; `goose` migrations; `slog` + OTel from M1. One module `backend/`, entrypoints under `cmd/`.
- **Proto:** `buf` lint + breaking-change checks in CI; `iris/v1` package; TS client via `protoc-gen-es`/`connect-es`.
- **Rust:** `engine/` cargo workspace; `engine-core` crate compiles from day 0 in CI (native + `wasm32-unknown-unknown`) even while mostly empty — keeps the seam honest.
- **Web:** pnpm workspace under `web/`; Vite + React 19 + TS strict; Zustand for UI state; generated `api-client` is never hand-edited.
- **Testing:** conformance suite for the inference spec; service tests against real Postgres (testcontainers); golden-frame perceptual diffs from M7; Playwright smoke for the signature-workflow exits.
- **Media:** ffmpeg via containerized workers only (no host-installed ffmpeg dependencies in prod paths).

## 6. Risks specific to this plan

| Risk | Mitigation |
|---|---|
| Two studios + engine work with 1–2 people stalls mid-M4/M5 | Slices are cuttable: M4 can ship without text layers/AI select; M5 without slip/slide. The W-moment exit criteria are the only hard line. |
| R&D endpoint slips past M5 | Everything runs on mock; dogfood quality checkpoints shift, schedule doesn't. Seedance adapter (M6) provides a real-model fallback for video dogfooding. |
| Contract churn after freeze (spec v1.0 wrong in practice) | Versioned manifests + buf breaking-change CI make evolution cheap; the rule is *versioned change*, not *no change*. |
| Doc-op vocabulary (M3) designed wrong → M4/M5 rework | Spike the op design against BOTH canvas and timeline cases on paper before M3 build; it's the one contract that spans three milestones. |
| Playback engine (M5) is the known hardest component | It gets the largest single allocation, ships proxy-only, and has the server-preview-render escape hatch designed in (TDD §2.3). |
