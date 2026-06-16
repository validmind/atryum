ARG GO_LICENSES=github.com/google/go-licenses/v2@v2.0.1
ARG LICENSE_CHECKER=license-checker@25.0.1
ARG ALLOWED_GO_LICENSES=Apache-2.0,BSD-3-Clause,CC-BY-4.0,ISC,MIT,0BSD,Unlicense
ARG ALLOWED_NPM_LICENSES=Apache-2.0;BSD-3-Clause;CC-BY-4.0;ISC;MIT;0BSD;Unlicense

FROM node:22-alpine AS ui-builder
ARG LICENSE_CHECKER
ARG ALLOWED_NPM_LICENSES
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
COPY scripts/copy_npm_license_files.js /tmp/copy_npm_license_files.js
RUN npm ci
RUN mkdir -p /third_party_notices/licenses/npm \
  && npx --yes "${LICENSE_CHECKER}" --summary --excludePrivatePackages --onlyAllow "${ALLOWED_NPM_LICENSES}" --start . \
  && npx --yes "${LICENSE_CHECKER}" --production --summary --excludePrivatePackages --onlyAllow "${ALLOWED_NPM_LICENSES}" --start . \
  && npx --yes "${LICENSE_CHECKER}" --production --json --excludePrivatePackages --onlyAllow "${ALLOWED_NPM_LICENSES}" --start . --out /third_party_notices/npm-production-licenses.json \
  && node /tmp/copy_npm_license_files.js /third_party_notices/npm-production-licenses.json /third_party_notices/licenses/npm
COPY ui/ ./
RUN npm run build

FROM golang:1.25-alpine AS builder
ARG GO_LICENSES
ARG ALLOWED_GO_LICENSES
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui-builder /ui/dist ./internal/api/web
COPY --from=ui-builder /third_party_notices/ /third_party_notices/
RUN mkdir -p /third_party_notices/licenses/go \
  && cp LICENSE /third_party_notices/ATRYUM_LICENSE \
  && cp NOTICE /third_party_notices/ATRYUM_NOTICE \
  && go run "${GO_LICENSES}" check --ignore atryum --allowed_licenses "${ALLOWED_GO_LICENSES}" ./... \
  && go run "${GO_LICENSES}" csv --ignore atryum ./... > /third_party_notices/go-licenses.csv \
  && go run "${GO_LICENSES}" save --ignore atryum --save_path /third_party_notices/licenses/go --force ./... \
  && printf '%s\n' \
    'Atryum Third-Party Notices' \
    '==========================' \
    '' \
    'This bundle contains third-party dependency license metadata and license files for the distributed Atryum binary and embedded UI. It also includes Atryum'\''s own LICENSE and NOTICE files for single-binary distributions.' \
    '' \
    'Atryum license: ATRYUM_LICENSE' \
    'Atryum notice: ATRYUM_NOTICE' \
    'Go dependency inventory: go-licenses.csv' \
    'Go license files: licenses/go/' \
    'npm production dependency inventory: npm-production-licenses.json' \
    'npm production license file index: npm-production-license-files.tsv' \
    'npm production license files: licenses/npm/' \
    '' \
    'The project LICENSE and NOTICE files should be distributed alongside this file.' \
    > /third_party_notices/THIRD_PARTY_NOTICES \
  && go run ./scripts/embed_notices.go /third_party_notices ./cmd/atryum/licenses_gen.go
RUN CGO_ENABLED=0 go build -tags release_notices -o /atryum ./cmd/atryum

FROM node:22-alpine 
COPY LICENSE NOTICE /usr/share/doc/atryum/
COPY --from=builder /third_party_notices/ /usr/share/doc/atryum/third_party/
COPY --from=ui-builder /third_party_notices/ /usr/share/doc/atryum/third_party/
COPY --from=builder /atryum /usr/local/bin/atryum
WORKDIR /app
ENTRYPOINT ["atryum", "run", "-config", "/app/atryum.toml"]
