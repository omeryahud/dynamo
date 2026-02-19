# Stage 1: Builder
FROM golang:1.23-alpine AS builder

# Set working directory
WORKDIR /workspace

# Copy go.mod and go.sum first for better layer caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy all source code
COPY . .

# Build static binary
# CGO_ENABLED=0: Create static binary
# GOOS=linux GOARCH=amd64: Target Linux AMD64
# -a: Force rebuild of packages
# -ldflags="-w -s": Strip debug info for smaller size
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags="-w -s" -o swap-coordinator main.go

# Stage 2: Runtime
FROM gcr.io/distroless/static-debian11:nonroot

# Set working directory
WORKDIR /

# Copy binary from builder
COPY --from=builder /workspace/swap-coordinator /swap-coordinator

# Expose HTTP API port
EXPOSE 8080

# Use non-root user (distroless already uses nonroot by default)
USER nonroot:nonroot

# Set entrypoint
ENTRYPOINT ["/swap-coordinator"]
