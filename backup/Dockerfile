# Build-Stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Installiere git, um private Go-Module zu holen (falls nötig)
RUN apk --no-cache add git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Baue das Backup-Tool als statische Binary
RUN CGO_ENABLED=0 GOOS=linux go build -v -o /backup-tool ./cmd/backup

# Final-Stage
FROM alpine:latest

# Installiere nur den PostgreSQL-Client für pg_dump
RUN apk --no-cache add postgresql-client

# Kopiere die kompilierte Binary aus dem Build-Stage
COPY --from=builder /backup-tool /backup-tool

# Führe das Backup-Tool direkt aus.
# Der Cron-Job wird nun vom Entrypoint des Docker-Compose-Services gesteuert.
ENTRYPOINT ["/backup-tool"]