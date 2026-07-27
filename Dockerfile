# Build stage
FROM golang:1.25-alpine3.23@sha256:cc985ef6f9c3bf9ece7488129c9abe0a150388ccdfa428d886fc709dca0b230a AS builder

ARG RACE_ENABLED=0

WORKDIR /app

# Install git for go mod download (some dependencies may need it)
# Install gcc + musl-dev when race detection is enabled (requires CGO)
RUN apk add --no-cache git=2.52.0-r0 && \
    if [ "$RACE_ENABLED" = "1" ]; then \
      apk add --no-cache gcc=15.2.0-r2 musl-dev=1.2.5-r23; \
    fi

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with version info
# Race detection requires CGO_ENABLED=1; strip flags (-s -w) omitted for race
# builds to preserve stack trace quality in race reports.
ARG VERSION=dev
ARG COMMIT=unknown
RUN set -e; \
    if [ "$RACE_ENABLED" = "1" ]; then \
      CGO_ENABLED=1 GOOS=linux go build -race \
        -ldflags="-X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /cronlite ./cmd/cronlite; \
    else \
      CGO_ENABLED=0 GOOS=linux go build \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /cronlite ./cmd/cronlite; \
    fi

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /cronlite-mcp ./cmd/cronlite-mcp

# Runtime stage
FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40

# Install runtime dependencies at reviewed versions for reproducible builds.
RUN apk add --no-cache ca-certificates=20260611-r0 tzdata=2026c-r0

# Create non-root user
RUN addgroup -g 1000 cronlite && \
    adduser -u 1000 -G cronlite -s /bin/sh -D cronlite

WORKDIR /app

# Copy binary from builder
COPY --from=builder /cronlite /usr/local/bin/cronlite
COPY --from=builder /cronlite-mcp /usr/local/bin/cronlite-mcp

# Copy schema for reference (optional, useful for migrations)
COPY schema/ /app/schema/

USER cronlite

EXPOSE 8080

ENTRYPOINT ["cronlite"]
CMD ["serve"]
