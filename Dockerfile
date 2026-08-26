# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# Install ca-certificates for HTTPS calls to GitHub API
RUN apk --no-cache add ca-certificates git

WORKDIR /build

# Cache dependencies separately from source — Docker layer caching means
# 'go mod download' only reruns when go.mod/go.sum change, not on code changes.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a fully static binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -tags netgo \
    -ldflags='-s -w -extldflags=-static' \
    -o /dotsync-server \
    ./cmd/dotsync

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM scratch

# TLS root certificates (needed for GitHub OAuth API calls)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# The server binary
COPY --from=builder /dotsync-server /dotsync-server

# Migration files — loaded at startup from MIGRATIONS_PATH (default: ./migrations)
COPY --from=builder /build/migrations /migrations

EXPOSE 8080

# Run as the binary directly — no shell needed in scratch image
ENTRYPOINT ["/dotsync-server"]
