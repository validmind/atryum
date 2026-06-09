set shell := ["bash", "-cu"]

config := "./atryum.toml"
release_dir := "releases"

# List justfile targets
default:
    just --list

# Start docker compose dev stack with atryum/ui/postgres/keycloak
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

# Build local production-like atryum binary with the local UI embedded
build-prod: build-ui build

# Build the FOSS React UI and embed it in internal/api/web/
build-ui:
	#!/usr/bin/env bash
	set -euo pipefail
	(cd ui && npm install && npm run build)
	rm -rf internal/api/web
	mkdir -p internal/api/web
	cp -R ui/dist/. internal/api/web/

# Build local atryum binary after building the FOSS UI
build-with-ui: build-ui build

# Dev server for the FOSS UI (proxies API calls to localhost:8080)
ui-dev:
	cd ui && npm install && npm run dev

# Run atryum process locally
run:
	go run ./cmd/atryum run --config {{config}}

# Kill local running atryum process
stop:
	pkill -f '/atryum run .*--config|go run ./cmd/atryum run'

# Build and push release artifacts for a tag
release tag:
        #!/usr/bin/env bash
        set -euo pipefail

        if [ -z "{{tag}}" ]; then
          echo "Usage: just release <tag>"
          exit 1
        fi

        just release-build "{{tag}}"
        just release-push "{{tag}}"

# Build release artifacts into releases/<tag>/
release-build tag: build-ui
        #!/usr/bin/env bash
        set -euo pipefail

        if [ -z "{{tag}}" ]; then
          echo "Usage: just release-build <tag>"
          exit 1
        fi

        repo_dir="$(pwd)"
        release_dir="$repo_dir/{{release_dir}}/{{tag}}"
        rm -rf "$release_dir"
        mkdir -p "$release_dir"

        build_target() {
          local goos="$1"
          local goarch="$2"
          local out="atryum-${goos}-${goarch}"

          tmp_dir="$(mktemp -d)"
          trap 'rm -rf "$tmp_dir"' RETURN

          mkdir -p "$tmp_dir/atryum"
          rsync -a --delete \
            --exclude .git \
            --exclude /atryum \
            --exclude /atryum.db \
            --exclude /{{release_dir}} \
            --exclude /ui/node_modules \
            --exclude /ui/dist \
            "$repo_dir/" "$tmp_dir/atryum/"

          (cd "$tmp_dir/atryum" && GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -o "$release_dir/$out" ./cmd/atryum)
        }

        # Build targets
        build_target darwin amd64
        build_target darwin arm64
        build_target linux amd64
        build_target linux arm64

# Create or update a GitHub release from releases/<tag>/
release-push tag:
        #!/usr/bin/env bash
        set -euo pipefail

        if [ -z "{{tag}}" ]; then
          echo "Usage: just release-push <tag>"
          exit 1
        fi

        release_dir="$(pwd)/{{release_dir}}/{{tag}}"
        shopt -s nullglob
        artifacts=("$release_dir"/atryum-*)

        if [ "${#artifacts[@]}" -eq 0 ]; then
          echo "No release artifacts found in $release_dir. Run: just release-build {{tag}}"
          exit 1
        fi

        # Create or upload to release
        if gh release view "{{tag}}" >/dev/null 2>&1; then
          gh release upload "{{tag}}" "${artifacts[@]}" --clobber
        else
          gh release create "{{tag}}" "${artifacts[@]}" --generate-notes
        fi
