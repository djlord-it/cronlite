# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install git for go mod download (some dependencies may need it)
RUN apk add --no-cache git

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with version info
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /easycron ./cmd/easycron

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /easycron-mcp ./cmd/easycron-mcp

# Runtime stage
FROM alpine:3.19

# Install ca-certificates for HTTPS webhook calls
RUN apk add --no-cache ca-certificates tzdata

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
