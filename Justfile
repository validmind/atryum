set shell := ["bash", "-cu"]

config := "./atryum.toml"

default:
    just --list

setup:
	go mod tidy

test:
	go test ./...

fmt:
	gofmt -w cmd internal

run:
	go run ./cmd/atryum -config {{config}}

stop:
	pkill -f '/atryum -config|go run ./cmd/atryum'

check: fmt test

up:
	docker compose --profile dev up -d --wait --build

down:
	docker compose --profile dev down

logs:
  docker compose --profile dev logs --follow

pg-reset:
	docker compose down -v
	docker compose up -d --wait

pg-logs:
	docker compose logs -f postgres
