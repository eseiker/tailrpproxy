# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/tailrpproxy ./cmd/tailrpproxy

FROM alpine:3.23

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 tailrpproxy \
    && adduser -S -D -H -u 65532 -G tailrpproxy tailrpproxy \
    && mkdir -p /var/lib/tailrpproxy \
    && chown 65532:65532 /var/lib/tailrpproxy
COPY --from=build /out/tailrpproxy /usr/local/bin/tailrpproxy

ENV RPPROXY_TRANSPORT=auto \
    RPPROXY_HEALTH_LISTEN=:9002 \
    RPPROXY_TSNET_STATE_DIR=/var/lib/tailrpproxy

EXPOSE 9002
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/tailrpproxy"]
