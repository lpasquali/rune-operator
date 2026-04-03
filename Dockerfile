# syntax=docker/dockerfile:1.7

FROM golang:1.25.7-alpine AS builder
WORKDIR /workspace

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /workspace/bin/rune-operator .

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/bin/rune-operator /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
