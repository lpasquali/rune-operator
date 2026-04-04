# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.24
FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /workspace

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /workspace/bin/rune-operator .

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/rune-operator /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
