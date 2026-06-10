FROM node:22-alpine AS ui-builder
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN npm run build

FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui-builder /ui/dist ./internal/api/web
RUN CGO_ENABLED=0 go build -o /atryum ./cmd/atryum

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /atryum /usr/local/bin/atryum
WORKDIR /app
ENTRYPOINT ["atryum", "run", "-config", "/app/atryum.toml"]
