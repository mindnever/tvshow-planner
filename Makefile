BIN := bin/tvshow-planner

.PHONY: build run test lint docker

build:
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/tvshow-planner

# Local run without an OIDC provider: everyone is logged in as "dev".
run:
	DEV_AUTH_USER=dev SECRET_KEY=dev-only-secret-key go run ./cmd/tvshow-planner

test:
	go test ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

docker:
	docker build -t tvshow-planner .
