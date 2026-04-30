set shell := ["bash", "-cu"]

config := "./atryum.toml"

setup:
	go mod tidy

test:
	go test ./...

fmt:
	gofmt -w cmd internal

run:
	go run ./cmd/atryum -config {{config}}

check: fmt test
