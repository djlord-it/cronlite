FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -o /webhook-receiver .

FROM alpine:3.23
RUN apk upgrade --no-cache
COPY --from=builder /webhook-receiver /usr/local/bin/webhook-receiver
EXPOSE 8080
ENTRYPOINT ["webhook-receiver"]
