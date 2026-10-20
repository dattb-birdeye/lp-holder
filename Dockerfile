# Pure Go, no cgo: the old Carbon/Rust FFI decoders are gone.
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lp-holder-api ./cmd/lp-holder-api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lp-holder-cli ./cmd/lp-holder-cli

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
COPY --from=builder /out/ /usr/local/bin/
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/lp-holder-api"]
