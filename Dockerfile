# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install git (needed for go mod download)
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY *.go ./

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o bsky-likes-downloader .

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install ffmpeg for video downloads
RUN apk add --no-cache ffmpeg ca-certificates

# Copy the binary from builder
COPY --from=builder /app/bsky-likes-downloader .

# Create directories for downloads and cache
RUN mkdir -p /data/downloads

# Set default environment variables
ENV DOWNLOAD_DIR=/data/downloads
ENV CACHE_FILE=/data/downloaded_cache.txt

ENTRYPOINT ["./bsky-likes-downloader"]
