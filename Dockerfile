# Multi-stage build for plane-forge-bridge.
#
# Layout:
#   - `build`   compiles the static binary
#   - `test`    inherits the built layers and runs vet / race / govulncheck
#   - `runtime` distroless, copies only the binary
#
# CI builds the `test` target before publishing the runtime image; a
# failing test stage prevents a push. Layer ordering (go.mod/go.sum first,
# then the rest of the source) keeps dependency download cached across
# source-only changes.

# syntax=docker/dockerfile:1.7

ARG VERSION=dev

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG VERSION
WORKDIR /src

# Dependency layer: copy module files first so `go mod download` is cached
# until go.mod or go.sum actually changes.
COPY go.mod ./
# go.sum may not exist yet on a fresh skeleton; tolerate that.
COPY go.su[m] ./
RUN go mod download

# Source layer.
COPY . .

# Static, stripped, reproducible build.
RUN CGO_ENABLED=0 GOFLAGS=-trimpath \
    go build \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/plane-forge-bridge \
        ./cmd/plane-forge-bridge

# Test stage: re-uses the `build` layers so the same source the runtime
# binary will come from is what gets tested. govulncheck is installed
# inside this stage so it participates in the layer cache.
FROM build AS test
RUN go install golang.org/x/vuln/cmd/govulncheck@latest
RUN go vet ./...
RUN go test -race -count=1 ./...
RUN govulncheck ./...

# Runtime: distroless static, nonroot. The binary is the only thing here.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/plane-forge-bridge /plane-forge-bridge
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/plane-forge-bridge"]
