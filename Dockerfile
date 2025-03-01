FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN apk add --no-cache binutils
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o registry-proxy ./cmd/server
RUN strip registry-proxy

FROM alpine:3.21.3  
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/registry-proxy /registry-proxy
CMD ["/registry-proxy"]