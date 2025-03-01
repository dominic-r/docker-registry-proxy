FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o registry-proxy ./cmd/server

FROM alpine:3.21.3
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/registry-proxy /registry-proxy
CMD ["/registry-proxy"]