# syntax=docker/dockerfile:1
# One file, four images (--target larkbot | fakelark | fakemcp | fakegateway). The build context is the Gotato repo root,
# because go.mod replaces Gotato and its adapter modules with the local checkout (../.., ../../adapter/*). From the root:
#   docker build -f examples/larkbot/cluster/Dockerfile --target larkbot -t larkbot:dev .
FROM golang:1.27 AS build
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY go.mod go.sum ./
COPY adapter/mcp/go.mod adapter/mcp/go.sum adapter/mcp/
COPY examples/larkbot/go.mod examples/larkbot/go.sum examples/larkbot/
WORKDIR /src/examples/larkbot
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY . .
WORKDIR /src/examples/larkbot
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags=-s -o /out/larkbot . && \
    go build -ldflags=-s -o /out/fakelark ./cmd/fakelark && \
    go build -ldflags=-s -o /out/fakemcp ./cmd/fakemcp && \
    go build -ldflags=-s -o /out/fakegateway github.com/jinhuang712/gotato/test/fakegateway/cmd/fakegateway

# No shell, no package manager: a static binary and CA certificates, run as 65532 (non-root).
FROM scratch AS base
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532

FROM base AS larkbot
COPY --from=build /out/larkbot /larkbot
ENTRYPOINT ["/larkbot"]

FROM base AS fakelark
COPY --from=build /out/fakelark /fakelark
ENTRYPOINT ["/fakelark"]

FROM base AS fakemcp
COPY --from=build /out/fakemcp /fakemcp
ENTRYPOINT ["/fakemcp"]

FROM base AS fakegateway
COPY --from=build /out/fakegateway /fakegateway
ENTRYPOINT ["/fakegateway"]
