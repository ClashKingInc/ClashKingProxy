# ============================
# Stage 1: Build the Go Binary
# ============================
FROM golang:1.23-alpine AS builder

# Install necessary packages
RUN apk add --no-cache git

# Set environment variables for Go
ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum files to leverage Docker cache
COPY go.mod go.sum ./

# Download Go dependencies
RUN go mod download

# Copy the entire project source code
COPY . .

# Build the Go application
# The -o flag specifies the output binary name
RUN go build -o proxy-server main.go

# ============================
# Stage 2: Create the Runtime Image
# ============================
FROM alpine:latest

# Install CA certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Set the working directory
WORKDIR /root/

# Copy the binary from the builder stage
COPY --from=builder /app/proxy-server .


# Expose the port your proxy server listens on
EXPOSE 8080

# Command to run the executable
CMD ["./proxy-server"]