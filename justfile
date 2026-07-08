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
    cd web && pnpm install --frozen-lockfile && pnpm typecheck

# Check an inference endpoint against spec/inference-api.md (R&D runs this too)
conformance url token="dev":
    cd backend && go run ./cmd/conformance -url {{url}} -token {{token}} -failure-injection

# Run services locally (each in its own terminal)
api:
    cd backend && go run ./cmd/api

orchestrator:
    cd backend && go run ./cmd/orchestrator

web:
    cd web && pnpm dev
