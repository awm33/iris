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

**Remaining for M1 exit:** media-worker ingest probe (ffprobe → duration/fps/dims for video+audio, filmstrip thumbnail for video cards) — first consumer of the pg queue, which then carries straight into M2's orchestrator. Then M2: generation core against the mocks.

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
