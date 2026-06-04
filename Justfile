set shell := ["bash", "-cu"]

config := "./atryum.toml"
frontend_dir := "../frontend"

# List justfile targets
default:
    just --list

# Start docker compose dev stack with atryum/frontend/postgres/keycloak
up:
	docker compose --profile dev up -d --wait --build
	just logs

# Stop docker compose dev stack
down:
	docker compose --profile dev down

# Tail the logs of the docker compose dev stack
logs:
  docker compose --profile dev logs --follow

# Tidy go mods
setup:
	go mod tidy

# Run go tests
test:
	go test ./...

# Run gofmt on the go code
fmt:
	gofmt -w cmd internal

# Go fmt and test
check: fmt test

# Build local atryum binary with the currently embedded web assets
build:
	CGO_ENABLED=0 go build -o ./atryum ./cmd/atryum

# Build local production-like atryum binary with the sibling frontend embedded
build-prod:
	#!/usr/bin/env bash
	set -euo pipefail
	repo_dir="$(pwd)"
	frontend_dir="{{frontend_dir}}"
	tmp_dir="$(mktemp -d)"
	cleanup() {
	  rm -rf "$tmp_dir"
	}
	trap cleanup EXIT

	(cd "$frontend_dir" && npm run build:atryum)
	mkdir -p "$tmp_dir/atryum"
	rsync -a --delete \
	  --exclude .git \
	  --exclude /atryum \
	  --exclude /atryum.db \
	  "$repo_dir/" "$tmp_dir/atryum/"
	rm -rf "$tmp_dir/atryum/internal/api/web"
	mkdir -p "$tmp_dir/atryum/internal/api/web"
	cp -R "$frontend_dir/build-atryum/." "$tmp_dir/atryum/internal/api/web/"
	mv "$tmp_dir/atryum/internal/api/web/atryum.html" "$tmp_dir/atryum/internal/api/web/index.html"
	(cd "$tmp_dir/atryum" && CGO_ENABLED=0 go build -o "$repo_dir/atryum" ./cmd/atryum)

# Run atryum process locally
run:
	go run ./cmd/atryum -config {{config}}

# Kill local running atryum process
stop:
	pkill -f '/atryum -config|go run ./cmd/atryum'
