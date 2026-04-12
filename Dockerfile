# syntax=docker/dockerfile:1.7
#
# Multi-arch: compile on $BUILDPLATFORM (native builder = fast), emit a binary
# for $TARGETARCH (set by buildx --platform). CI builds amd64 + arm64 on native
# runners; local `docker build` must not pin linux/amd64 or arm64 hosts break.

ARG GO_VERSION=1.25
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder
WORKDIR /workspace

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags='-s -w' -o /workspace/bin/rune-operator .

FROM --platform=$TARGETPLATFORM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/rune-operator /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
