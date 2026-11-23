# syntax=docker/dockerfile:1
# Keep in sync with the toolchain directive in go.mod: the stdlib compiled into the binary
# comes from this image, and trivy scans it.
FROM --platform=$BUILDPLATFORM golang:1.27.4 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/orbit-agent ./cmd/orbit-agent && \
    mkdir -p /out/state

# Static binary, no shell, non-root: small attack surface on edge nodes.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/orbit-agent /orbit-agent
# State dir owned by nonroot so a mounted volume inherits the right ownership.
COPY --from=build --chown=65532:65532 /out/state /var/lib/orbit
USER nonroot:nonroot
ENTRYPOINT ["/orbit-agent"]
