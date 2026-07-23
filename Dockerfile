# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X gosend/internal/buildinfo.Version=${VERSION} -X gosend/internal/buildinfo.Commit=${COMMIT} -X gosend/internal/buildinfo.Date=${BUILD_DATE}" \
    -o /out/gosend ./cmd/gosend

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 gosend \
    && adduser -S -D -H -u 10001 -G gosend gosend \
    && mkdir -p /data/send /data/receive \
    && chown -R gosend:gosend /data
COPY --from=build /out/gosend /usr/local/bin/gosend
USER 10001:10001
VOLUME ["/data"]
EXPOSE 8080/tcp 53317/tcp 53317/udp
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/readyz || exit 1
ENTRYPOINT ["/usr/local/bin/gosend"]
CMD ["--data-dir", "/data"]
