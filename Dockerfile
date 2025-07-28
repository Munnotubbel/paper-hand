# Stage 1: Builder
FROM golang:1.24 AS builder
WORKDIR /app

# Abhängigkeiten herunterladen
COPY go.mod go.sum ./
RUN go mod download

# Quellcode kopieren und bauen
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags='-s -w' -o /paper-hand

# Stage 2: Final
FROM scratch

# Binärdatei aus dem Builder-Stage kopieren
COPY --from=builder /paper-hand /paper-hand

# Kommando zum Starten
ENTRYPOINT ["/paper-hand"]