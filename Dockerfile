FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/here-now-alt ./cmd/here-now-alt

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /data \
    && chown app:app /data

COPY --from=build /out/here-now-alt /usr/local/bin/here-now-alt

USER app:app
VOLUME ["/data"]
EXPOSE 8080
ENV PORT=8080 \
    DATA_DIR=/data \
    TTL=720h

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/here-now-alt"]
