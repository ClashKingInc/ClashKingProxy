FROM golang:1.26.2-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go build -o proxy .

FROM alpine:latest
WORKDIR /app
RUN apk add --no-cache curl ca-certificates
COPY --from=builder /app/proxy ./
EXPOSE 80

CMD ["./proxy"]
