set dotenv-load := true
# Iris dev commands. Install `just`: https://github.com/casey/just

# Bring up the dev stack (Postgres+pgvector, MinIO, mock model servers)
dev:
    docker compose -f deploy/docker-compose.dev.yml up -d --build

dev-down:
    docker compose -f deploy/docker-compose.dev.yml down

# Regenerate Go + TS from protos (requires buf: https://buf.build)
gen:
    cd proto && buf lint && buf generate

# Apply migrations (requires goose: github.com/pressly/goose)
migrate:
    goose -dir backend/migrations postgres "postgres://iris:iris@localhost:15432/iris?sslmode=disable" up

# Run everything's checks (mirrors CI)
check:
    cd backend && go vet ./... && go build ./... && go test ./...
    cd engine && cargo check --workspace && cargo test --workspace
    cd proto && buf lint
    cd web && pnpm install --frozen-lockfile && pnpm typecheck && pnpm test

# Check an inference endpoint against spec/inference-api.md (R&D runs this too)
conformance url token="dev":
    cd backend && go run ./cmd/conformance -url {{url}} -token {{token}} -failure-injection

# Run services locally (each in its own terminal)
api:
    cd backend && MOCK_SEEDANCE_KEY=${MOCK_SEEDANCE_KEY:-dev-seedance-key} MOCK_ELEVENLABS_KEY=${MOCK_ELEVENLABS_KEY:-dev-elevenlabs-key} go run ./cmd/api

# Dev runs with the generation cache ON: identical requests replay landed
# artifacts (free) — unset IRIS_GEN_CACHE to exercise real dispatch.
orchestrator:
    cd backend && IRIS_GEN_CACHE=${IRIS_GEN_CACHE:-1} MOCK_SEEDANCE_KEY=${MOCK_SEEDANCE_KEY:-dev-seedance-key} MOCK_ELEVENLABS_KEY=${MOCK_ELEVENLABS_KEY:-dev-elevenlabs-key} go run ./cmd/orchestrator

# Recorded-shape Seedance mock (loopback) — the seedance adapter's dev target.
mock-seedance:
    cd backend && go run ./cmd/mock-seedance

# Recorded-shape ElevenLabs mock (loopback) — the elevenlabs adapter's dev target.
mock-elevenlabs:
    cd backend && go run ./cmd/mock-elevenlabs

worker:
    cd backend && go run ./cmd/media-worker

web:
    cd web && pnpm dev

# Golden-frame parity: preview vs export, per-channel budget (needs the
# full dev stack + `npx playwright install chromium` once).
golden:
    cd tools/golden && npm install --silent && node golden.mjs
