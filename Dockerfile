# syntax=docker/dockerfile:1.7
#
# arbox-scheduler container image.
#
# Stage 1: build a fully static Linux binary with the Go toolchain.
# Stage 2: ship it on Alpine so we get a real shell + tzdata + ca-certs for
# debugging via `fly ssh console`, while staying under ~20 MB total.
#
# Runtime layout for any container host (/data is a common volume convention):
#   /app/arbox        — the binary
#   /app/config.yaml  — baked-in config (override by mounting a file)
#   /data/.env        — persisted tokens (ARBOX_ENV_FILE points here)
#   TZ=Asia/Jerusalem — so window math matches Arbox's wall clock

ARG GO_VERSION=1.25

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src

# Cache module downloads — re-run only when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Cross-compile for the target arch so this image works whether the host gives us
# amd64 or arm64 machines. CGO is off so the binary is fully static.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /out/arbox ./cmd/arbox

# ---- runtime stage -----------------------------------------------------------

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S arbox \
    && adduser  -S -G arbox arbox \
    && mkdir -p /data \
    && chown arbox:arbox /data

WORKDIR /app
COPY --from=builder /out/arbox /app/arbox
COPY config.yaml /app/config.yaml

ENV TZ=Asia/Jerusalem \
    ARBOX_ENV_FILE=/data/.env

USER arbox
VOLUME ["/data"]

ENTRYPOINT ["/app/arbox"]
CMD ["daemon"]
