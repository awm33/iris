# Iris

Integrated AI image + video editor: Photoshop/Premiere-class editing fused with generative workflows (scenes, sets, characters, shots, takes), grounded in our own Wan/Qwen-derived models plus self-hosted open-weight and commercial API models.

**Start here:** [docs/design/01-high-level-design.md](docs/design/01-high-level-design.md) → [02 UI/UX](docs/design/02-ui-ux-design.md) → [03 Technical](docs/design/03-technical-design.md) → [04 Implementation Plan](docs/design/04-implementation-plan.md) · Market research: [docs/research/](docs/research/2026-07-market-and-model-landscape.md)

## Repo map

```
docs/          Design docs + research
spec/          The R&D contract: inference-api.md + manifest.schema.json (model endpoints implement this)
proto/         Protobuf schemas (buf) — single source of truth for the app API (Connect-RPC)
backend/       Go services: cmd/api, cmd/orchestrator, cmd/media-worker, cmd/mock-model; migrations/
engine/        Rust engine-core crate (frame graph / compositing; WASM + native targets)
web/           pnpm workspace: apps/app (React), packages/api-client (generated — never hand-edit)
deploy/        docker-compose.dev.yml (Postgres+pgvector, MinIO, mock model servers)
```

## Quickstart

Prereqs: Docker, Go 1.25+, Rust (stable + `wasm32-unknown-unknown`), Node 22 + pnpm 9, [`just`](https://github.com/casey/just), [`buf`](https://buf.build), [`goose`](https://github.com/pressly/goose).

```sh
just dev        # Postgres, MinIO, mock model servers (video :8900, image :8901)
just migrate    # apply backend/migrations
just gen        # protos → Go (backend/gen) + TS (web/packages/api-client/src/gen)
just api        # run the API        (separate terminal)
just web        # run the frontend   (separate terminal)
just check      # everything CI runs
```

Poke the mock model:

```sh
curl -s -H "Authorization: Bearer dev" localhost:8900/v1/manifest | jq .id
curl -s -X POST -H "Authorization: Bearer dev" -d '{"id":"j_demo1","task":"t2v","prompt":"a diner at night"}' localhost:8900/v1/jobs | jq
curl -s -H "Authorization: Bearer dev" localhost:8900/v1/jobs/j_demo1 | jq .state
```

Check any inference endpoint against the spec (this is what R&D runs before integration):

```sh
just conformance http://localhost:8900   # 8 checks: manifest schema, auth, lifecycle + artifact sha256, idempotency, cancel, error taxonomy
```

## Rules of the repo

1. **No model research code here — ever.** Models are consumed only through `spec/inference-api.md` (see the R&D boundary, [TDD §1](docs/design/03-technical-design.md)).
2. **Contracts before code:** proto / spec / migrations changes get reviewed first; `buf breaking` guards the API.
3. **Generated code is never hand-edited** (`backend/gen`, `web/packages/api-client/src/gen`).
4. Remote: `github.com/awm33/iris` (may move under the bucreative entity later — module-path rename is mechanical).

Current milestone: **M0 → M1** (see the [implementation plan](docs/design/04-implementation-plan.md)).
