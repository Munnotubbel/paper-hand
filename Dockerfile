# Build-Stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -v -o paper-hand .

# Final-Stage
FROM alpine:latest

# Füge Root-Zertifikate für HTTPS-Anfragen hinzu
RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/paper-hand .

# Die Konfiguration wird über Umgebungsvariablen in docker-compose.prod.yml bereitgestellt
# COPY .env .

EXPOSE 4242

CMD ["./paper-hand"]