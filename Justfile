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

stop:
	pkill -f '/atryum -config|go run ./cmd/atryum'

check: fmt test
