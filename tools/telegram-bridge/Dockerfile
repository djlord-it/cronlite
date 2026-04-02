FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN go build -o /telegram-bridge .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /telegram-bridge /usr/local/bin/telegram-bridge
ENTRYPOINT ["telegram-bridge"]
