# Flanj Collector — multi-stage build.
#
#   1. ui      : node builds the Vue/Vite SPA -> dist/
#   2. build   : golang + ocb build the collector binary, embedding the SPA
#   3. runtime : distroless, static binary + the three role configs
#
# The version triad is pinned in builder-config.yaml (ocb v0.159.0, beta v0.159.0,
# stable v1.65.0). CGO is OFF — modernc.org/sqlite is pure Go — so the binary is
# static and runs on distroless/static.
#
# Multi-platform builds CROSS-COMPILE. Stages 1 and 2 are pinned to the machine
# doing the build ($BUILDPLATFORM) and run natively whatever `--platform` asks
# for; only stage 3 is the target platform. Because CGO is off, Go cross-compiles
# by setting GOOS/GOARCH — nothing below runs a foreign-architecture toolchain
# under emulation. The UI's dist/ is architecture-independent, so it is built once
# and shared by every target. A build stage that ran per target under QEMU took
# well over 15 minutes for the second platform (the ocb compile is a full
# OpenTelemetry Collector distribution).

# ---- 1. UI ----------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
WORKDIR /ui
COPY ui/package.json ui/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY ui/ ./
RUN npm run build   # -> /ui/dist

# ---- 2. build -------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
# Static, pure-Go: this is what makes GOOS/GOARCH below a complete cross-compile.
ENV CGO_ENABLED=0
WORKDIR /src

# Prime the module cache from the root + component manifests before copying all
# sources, so dependency downloads cache across rebuilds.
COPY go.mod go.sum ./
COPY processor ./processor
COPY exporter ./exporter
COPY extension ./extension
COPY internal ./internal
COPY contract ./contract
COPY contracts ./contracts
COPY config ./config
COPY builder-config.yaml ./

# Bring in the built SPA so go:embed all:web/dist embeds the real assets.
RUN rm -rf extension/flanjui/web/dist
COPY --from=ui /ui/dist ./extension/flanjui/web/dist

# The builder is installed for, and runs on, the BUILD platform — GOOS/GOARCH are
# deliberately not set until the compile step, or it would be built for the target
# and fail to execute here.
RUN go install go.opentelemetry.io/collector/cmd/builder@v0.159.0

# Release version stamped into the binary (GET /api/health `collector_version`,
# X-Flanj-Collector-Version on flag POSTs). Set with
# `docker build --build-arg VERSION=v0.6.0 .`; unset builds report "dev".
# TARGETOS/TARGETARCH are BuildKit's automatic per-platform args. They are declared
# here, after everything both targets share, so those layers are built once.
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

# Compile the distribution for the target. ocb passes its environment through to
# `go build`, so GOOS/GOARCH set on this one command retarget only the compile.
# Passing --ldflags REPLACES ocb's default ("-s -w"), so restate it alongside the
# version stamp.
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} builder --config builder-config.yaml \
    --ldflags="-s -w -X github.com/flanj-io/collector/extension/flanjui.collectorVersion=${VERSION}"

# ---- 3. runtime -----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /src/_build/flanj-collector /flanj-collector
# The image bakes a NEUTRAL config, not the annotated example: the example's
# illustrative identity made a stranger's first run come up as somebody else's
# org (issue #55). config.default.yaml carries no identity; it does name the
# hosted control plane, and sends nothing to it until Connect mints a key.
COPY --from=build /src/config/config.default.yaml /etc/flanj/config.yaml
# Tiered-topology role configs (docs/STORE.md "Topologies"): select a role with
# `--config /etc/flanj/front.yaml` (N stateless fronts: otlp -> redaction ->
# drift -> otlphttp) or `--config /etc/flanj/store.yaml` (the ONE store pod:
# otlp -> redaction -> store + UI). The default CMD stays the single-pod config.
COPY --from=build /src/config/config.front.example.yaml /etc/flanj/front.yaml
COPY --from=build /src/config/config.store.example.yaml /etc/flanj/store.yaml
# NOTE: /etc/flanj/spec-v1.yaml and spec-v2.yaml were baked here until 2026-09-02
# and are deliberately gone. Nothing read them at runtime: provider contracts are
# UPLOADED in the UI and read from the store (CONTRACTS §8, 2026-08-31), and the
# `spec_path` keys that once pointed at them no longer exist. They remain in
# contracts/ as test fixtures, which is the only thing that ever used them.

# Provenance. REVISION above all: without it nothing in a pulled image says
# which commit produced it, and the v0.1.0 publish (by hand, from a laptop)
# recorded that nowhere at all. The release workflow passes all three; a local
# `docker build` with no args gets honest placeholders rather than a wrong claim.
# ARGs are redeclared here because the ones above belong to the build stage.
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=
LABEL org.opencontainers.image.title="Flanj Collector" \
      org.opencontainers.image.description="Integration-reliability collector: captures and redacts third-party API traffic near source, detects drift against provider contracts, and serves a localhost UI." \
      org.opencontainers.image.url="https://github.com/flanj-io/collector" \
      org.opencontainers.image.source="https://github.com/flanj-io/collector" \
      org.opencontainers.image.documentation="https://github.com/flanj-io/collector#readme" \
      org.opencontainers.image.licenses="Elastic-2.0" \
      org.opencontainers.image.vendor="Flanj" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"

# OTLP/HTTP ingest. The UI (127.0.0.1:5335) is loopback-only and deliberately
# NOT exposed — reach it via `kubectl port-forward` / an SSH tunnel.
EXPOSE 4318

# /data is the persistent volume (PVC) mount point for the embedded store.
VOLUME ["/data"]

ENTRYPOINT ["/flanj-collector"]
CMD ["--config", "/etc/flanj/config.yaml"]
