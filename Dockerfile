# Build stage
FROM golang:1.25-alpine3.23 AS builder

ARG RACE_ENABLED=0

WORKDIR /app

# Install git for go mod download (some dependencies may need it)
# Install gcc + musl-dev when race detection is enabled (requires CGO)
RUN apk add --no-cache git && \
    if [ "$RACE_ENABLED" = "1" ]; then apk add --no-cache gcc musl-dev; fi

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
        -o /easycron ./cmd/easycron; \
    else \
      CGO_ENABLED=0 GOOS=linux go build \
        -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
        -o /easycron ./cmd/easycron; \
    fi

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /easycron-mcp ./cmd/easycron-mcp

# Runtime stage
FROM alpine:3.23

# Refresh base packages so Trivy sees the latest security fixes from the
# selected Alpine branch, then install runtime dependencies.
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 easycron && \
    adduser -u 1000 -G easycron -s /bin/sh -D easycron

WORKDIR /app

# Copy binary from builder
COPY --from=builder /easycron /usr/local/bin/easycron
COPY --from=builder /easycron-mcp /usr/local/bin/easycron-mcp

# Copy schema for reference (optional, useful for migrations)
COPY schema/ /app/schema/

USER easycron

EXPOSE 8080

ENTRYPOINT ["easycron"]
CMD ["serve"]
