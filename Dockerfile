FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY server.go ./
RUN go build -o proxy server.go

FROM alpine:latest
WORKDIR /app
ENV HOST=0.0.0.0 PORT=8011
RUN apk add --no-cache curl ca-certificates
COPY --from=builder /app/proxy ./
EXPOSE 8011

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD curl -f http://127.0.0.1:${PORT}/ || exit 1

CMD ["./proxy"]