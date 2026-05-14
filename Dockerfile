FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /atryum ./cmd/atryum

FROM node:22-alpine
RUN apk add --no-cache ca-certificates
COPY --from=builder /atryum /usr/local/bin/atryum
WORKDIR /app
ENTRYPOINT ["atryum", "-config", "/app/atryum.toml"]
