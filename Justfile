set shell := ["bash", "-cu"]

config := "./atryum.toml"

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

# Run atryum process locally
run:
	go run ./cmd/atryum -config {{config}}

# Kill local running atryum process
stop:
	pkill -f '/atryum -config|go run ./cmd/atryum'
