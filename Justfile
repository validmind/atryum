set shell := ["bash", "-cu"]

config := "./atryum.toml"
release_dir := "releases"
integration_image := "atryum-integrations"
license_dir := "license-reports"
go_licenses := "github.com/google/go-licenses/v2@v2.0.1"
license_checker := "license-checker@25.0.1"
allowed_go_licenses := "Apache-2.0,BSD-3-Clause,CC-BY-4.0,ISC,MIT,0BSD,Unlicense"
allowed_npm_licenses := "Apache-2.0;BSD-3-Clause;CC-BY-4.0;ISC;MIT;0BSD;Unlicense"

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

# Generate Go and UI third-party dependency license reports
licenses: licenses-go licenses-ui
	@echo "License reports written to {{license_dir}}/"

# Generate Go dependency license inventory and enforce the approved license allowlist
licenses-go:
	#!/usr/bin/env bash
	set -euo pipefail
	mkdir -p "{{license_dir}}"
	go run {{go_licenses}} csv --ignore atryum ./... > "{{license_dir}}/go-licenses.csv"
	go run {{go_licenses}} check --ignore atryum --allowed_licenses "{{allowed_go_licenses}}" ./...

# Generate UI dependency license inventories and summaries
licenses-ui:
	#!/usr/bin/env bash
	set -euo pipefail
	mkdir -p "{{license_dir}}"
	(cd ui && npm install --ignore-scripts --no-audit --no-fund)
	(cd ui && npx --yes {{license_checker}} --json --excludePrivatePackages --start . --out "../{{license_dir}}/ui-licenses.json")
	(cd ui && npx --yes {{license_checker}} --summary --excludePrivatePackages --onlyAllow "{{allowed_npm_licenses}}" --start .) | tee "{{license_dir}}/ui-license-summary.txt"
	(cd ui && npx --yes {{license_checker}} --production --json --excludePrivatePackages --start . --out "../{{license_dir}}/ui-production-licenses.json")
	(cd ui && npx --yes {{license_checker}} --production --summary --excludePrivatePackages --onlyAllow "{{allowed_npm_licenses}}" --start .) | tee "{{license_dir}}/ui-production-license-summary.txt"

# Generate third-party notice and license-file bundle for release artifacts
third-party-notices:
	GO_LICENSES="{{go_licenses}}" \
	LICENSE_CHECKER="{{license_checker}}" \
	ALLOWED_GO_LICENSES="{{allowed_go_licenses}}" \
	ALLOWED_NPM_LICENSES="{{allowed_npm_licenses}}" \
	GO_NOTICE_FILE="cmd/atryum/licenses_gen.go" \
	  scripts/generate_third_party_notices.sh "{{license_dir}}/third-party"

# Build local atryum binary with the currently embedded web assets
build:
	CGO_ENABLED=0 go build -o ./atryum ./cmd/atryum

# Remove generated binaries, release artifacts, built UI assets, and integration test debris
clean:
	rm -rf ./atryum {{release_dir}} {{license_dir}} ui/dist internal/api/web \
	  cmd/atryum/licenses_gen.go \
	  integrations/.venv integrations/.run integrations/.harness-config integrations/results \
	  integrations/*.db integrations/*.db-journal integrations/*.log integrations/*.pid

# Build local production-like atryum binary with the local UI embedded
build-prod: third-party-notices build-ui
	CGO_ENABLED=0 go build -tags release_notices -o ./atryum ./cmd/atryum

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

# Build documentation HTML from md-drafts/
docs:
	python3 website/scripts/md_to_html.py

# Build documentation PDF from md-drafts/
docs-pdf:
	python3 website/scripts/md_to_pdf.py

# Regenerate docs, then serve the website locally
preview-docs: docs
	python3 -m http.server 8000 --directory website

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
release-build tag: third-party-notices build-ui
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
        cp LICENSE NOTICE "$release_dir/"
        cp "{{license_dir}}/third-party/THIRD_PARTY_NOTICES" "$release_dir/"
        cp -R "{{license_dir}}/third-party/licenses" "$release_dir/third_party_licenses"
        cp "{{license_dir}}/third-party/go-licenses.csv" "$release_dir/"
        cp "{{license_dir}}/third-party/npm-production-licenses.json" "$release_dir/"
        cp "{{license_dir}}/third-party/npm-production-license-files.tsv" "$release_dir/"

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

          (cd "$tmp_dir/atryum" && GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -tags release_notices -o "$release_dir/$out" ./cmd/atryum)
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

# Run the LLM-as-judge grounding eval against any OpenAI-compatible endpoint.
# Session history is always reconstructed and fenced, matching production.
# base_url is the server ROOT — no /v1; the runner appends /v1/chat/completions.
# api_key may be empty for keyless local servers (e.g. Ollama).
# Examples:
#   just judge-eval                                                       # litellm at :4000, gpt-5.4-mini
#   just judge-eval model=llama3.1 base_url=http://localhost:11434        # Ollama, keyless
#   just judge-eval model=gpt-4o base_url=https://api.openai.com api_key="$OPENAI_API_KEY"
# Results land in internal/invocation/testdata/judge_evals/results/<model>.{json,md}
judge-eval model="gpt-5.4-mini" base_url="http://localhost:4000" api_key="" trials="1":
	ATRYUM_JUDGE_EVAL_MODEL="{{model}}" \
	ATRYUM_JUDGE_EVAL_BASE_URL="{{base_url}}" \
	ATRYUM_JUDGE_EVAL_API_KEY="{{api_key}}" \
	ATRYUM_JUDGE_EVAL_TRIALS="{{trials}}" \
	  go test -tags judgeeval ./internal/invocation -run TestJudgeGrounding -v

# Verify the eval harness and corpus load with no LLM or API key (fail-closed
# contract tests + the constant-verdict baseline floor). Fast, offline, deterministic.
judge-eval-check:
	go test -tags judgeeval ./internal/invocation \
	  -run 'TestJudge(GarbageOutput|MarkdownFenced|Request|UnrecognizedVerdict)|TestConstantVerdictBaselines' -v

# List registered harnesses, auth protocols, and MCP targets
integration-list:
	integrations/scripts/agent_harness_integration_tests.sh list

# Run a single integration case (override harness/auth/target via env or args)
integration-test harness="fake-agent" auth="no-auth" target="calculator":
	integrations/scripts/agent_harness_integration_tests.sh run \
	  --harness {{harness}} --auth {{auth}} --target {{target}}

# Run the full integration matrix (skips unavailable harnesses and placeholder auth)
integration-test-matrix *args:
	integrations/scripts/agent_harness_integration_tests.sh matrix --only-passing {{args}}

# Build Docker image for integration tests
integration-docker-build:
	docker build -f Dockerfile.integrations -t {{integration_image}} .

# Run integration tests inside the Docker image
integration-docker-test *args:
	docker run --rm \
	  -e OPENAI_API_KEY \
	  -e CODEX_API_KEY \
	  -e ANTHROPIC_API_KEY \
	  -e AMP_API_KEY \
	  -e XAI_API_KEY \
	  {{integration_image}} {{args}}
