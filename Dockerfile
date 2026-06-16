ARG GO_LICENSES=github.com/google/go-licenses/v2@v2.0.1
ARG LICENSE_CHECKER=license-checker@25.0.1
ARG ALLOWED_GO_LICENSES=Apache-2.0,BSD-3-Clause,CC-BY-4.0,ISC,MIT,0BSD,Unlicense
ARG ALLOWED_NPM_LICENSES=Apache-2.0;BSD-3-Clause;CC-BY-4.0;ISC;MIT;0BSD;Unlicense

FROM node:22-alpine AS ui-builder
ARG LICENSE_CHECKER
ARG ALLOWED_NPM_LICENSES
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
RUN npx --yes "${LICENSE_CHECKER}" --summary --excludePrivatePackages --onlyAllow "${ALLOWED_NPM_LICENSES}" --start . \
  && npx --yes "${LICENSE_CHECKER}" --production --summary --excludePrivatePackages --onlyAllow "${ALLOWED_NPM_LICENSES}" --start .
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
RUN go run "${GO_LICENSES}" check --ignore atryum --allowed_licenses "${ALLOWED_GO_LICENSES}" ./...
RUN CGO_ENABLED=0 go build -o /atryum ./cmd/atryum

FROM node:22-alpine 
COPY --from=builder /atryum /usr/local/bin/atryum
WORKDIR /app
ENTRYPOINT ["atryum", "run", "-config", "/app/atryum.toml"]
