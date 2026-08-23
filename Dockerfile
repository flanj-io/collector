# Vinifera Collector — multi-stage build.
#
#   1. ui      : node builds the Vue/Vite SPA -> dist/
#   2. build   : golang + ocb build the collector binary, embedding the SPA
#   3. runtime : distroless, static binary + config + specs
#
# The version triad is pinned in builder-config.yaml (ocb v0.159.0, beta v0.159.0,
# stable v1.65.0). CGO is OFF — modernc.org/sqlite is pure Go — so the binary is
# static and runs on distroless/static.

# ---- 1. UI ----------------------------------------------------------------
FROM node:22-alpine AS ui
WORKDIR /ui
COPY ui/package.json ui/package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY ui/ ./
RUN npm run build   # -> /ui/dist

# ---- 2. build -------------------------------------------------------------
FROM golang:1.26 AS build
ENV CGO_ENABLED=0
WORKDIR /src

# Prime the module cache from the root + component manifests before copying all
# sources, so dependency downloads cache across rebuilds.
COPY go.mod go.sum ./
COPY processor ./processor
COPY exporter ./exporter
COPY extension ./extension
COPY internal ./internal
COPY contracts ./contracts
COPY config ./config
COPY builder-config.yaml ./

# Bring in the built SPA so go:embed all:web/dist embeds the real assets.
RUN rm -rf extension/viniferaui/web/dist
COPY --from=ui /ui/dist ./extension/viniferaui/web/dist

# Install the pinned builder and compile the distribution.
RUN go install go.opentelemetry.io/collector/cmd/builder@v0.159.0
RUN builder --config builder-config.yaml

# ---- 3. runtime -----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /src/_build/vinifera-collector /vinifera-collector
COPY --from=build /src/config/config.example.yaml /etc/vinifera/config.yaml
# Tiered-topology role configs (docs/STORE.md "Topologies"): select a role with
# `--config /etc/vinifera/front.yaml` (N stateless fronts: otlp -> redaction ->
# drift -> otlphttp) or `--config /etc/vinifera/store.yaml` (the ONE store pod:
# otlp -> redaction -> store + UI). The default CMD stays the single-pod config.
COPY --from=build /src/config/config.front.example.yaml /etc/vinifera/front.yaml
COPY --from=build /src/config/config.store.example.yaml /etc/vinifera/store.yaml
COPY --from=build /src/contracts/spec-v1.yaml /etc/vinifera/spec-v1.yaml
COPY --from=build /src/contracts/spec-v2.yaml /etc/vinifera/spec-v2.yaml

# OTLP/HTTP ingest. The UI (127.0.0.1:5335) is loopback-only and deliberately
# NOT exposed — reach it via `kubectl port-forward` / an SSH tunnel.
EXPOSE 4318

# /data is the persistent volume (PVC) mount point for the embedded store.
VOLUME ["/data"]

ENTRYPOINT ["/vinifera-collector"]
CMD ["--config", "/etc/vinifera/config.yaml"]
