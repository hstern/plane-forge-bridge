# Multi-stage build for the plane-stub recording service.
#
# Build this image from the REPOSITORY ROOT (not from this directory), so the
# `COPY . .` step below has access to go.mod, go.sum, and the package source.
#
#   docker build -f test/e2e-docker/plane-stub/Dockerfile -t plane-stub .

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/plane-stub ./test/e2e-docker/plane-stub

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/plane-stub /plane-stub
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/plane-stub"]
