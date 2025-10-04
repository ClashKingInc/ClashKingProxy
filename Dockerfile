# --- build stage ---
FROM golang:1.25-alpine AS builder
WORKDIR /app

# Add build tools
RUN apk add --no-cache git ca-certificates

# Copy source
COPY . .

# Build static binary
RUN go build -ldflags="-s -w" -o coc-proxy .

# --- final runtime stage ---
FROM alpine:3.19 AS final
WORKDIR /app

# Add CA certs for HTTPS
RUN apk add --no-cache ca-certificates curl

# Copy binary from builder
COPY --from=builder /app/coc-proxy .

# Expose port
EXPOSE 8011

# Healthcheck
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD curl -f http://127.0.0.1:${PORT:-8011}/ || exit 1

# Default env
ENV HOST=0.0.0.0 PORT=8011

CMD ["./coc-proxy"]
