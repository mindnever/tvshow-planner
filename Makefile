BIN := bin/tvshow-planner

.PHONY: build run test lint docker generate

# Regenerate internal/db/sqlcgen after editing migrations or queries.sql.
generate:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate

build:
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/tvshow-planner

# Local run; point JELLYFIN_URL at your server, e.g. make run JELLYFIN_URL=http://localhost:8096
run:
	JELLYFIN_URL=$(JELLYFIN_URL) SECRET_KEY=dev-only-secret-key go run ./cmd/tvshow-planner

test:
	go test ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

docker:
	docker build -t tvshow-planner .
