# Multi-stage build for ultra-tiny footprint (~18MB total)
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install git and ca-certificates
RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod ./
# Download dependencies
RUN go mod download || true

COPY . .

# Tidy dependencies and build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -extldflags '-static'" -o edgetunnel-lb ./cmd/server

# Final production image
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata
ENV TZ=Asia/Shanghai

WORKDIR /app

COPY --from=builder /app/edgetunnel-lb /app/edgetunnel-lb
COPY config.example.json /app/config.example.json

# Default ports
EXPOSE 8787 8788 8789

VOLUME ["/app/config.json", "/app/backups"]

ENTRYPOINT ["/app/edgetunnel-lb"]
CMD ["-config", "/app/config.json"]
