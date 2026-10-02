# Tellor Layer node image.
#
# The image contains no chain binary. It ships cosmovisor and layerctl; the actual
# layerd binaries are the official upstream release artifacts, downloaded at
# runtime into the cosmovisor layout and verified against the checksums published
# with each release. That is what lets one image serve every chain version without
# a rebuild, and what keeps us off a fork of the chain source.

# --- build layerctl and cosmovisor -------------------------------------------
FROM golang:1.25-bookworm AS build

# Pin cosmovisor so the supervisor cannot change under us on a rebuild.
ARG COSMOVISOR_VERSION=v1.7.1
RUN GOBIN=/out go install cosmossdk.io/tools/cosmovisor/cmd/cosmovisor@${COSMOVISOR_VERSION}

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/layerctl ./cmd/layerctl

# --- runtime ------------------------------------------------------------------
FROM debian:bookworm-slim

# ca-certificates is required: layerctl fetches releases over HTTPS.
# curl is here for the compose healthcheck, which polls the node's /status.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl tini \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/cosmovisor /usr/local/bin/cosmovisor
COPY --from=build /out/layerctl   /usr/local/bin/layerctl

# Put layerd on PATH for anything that shells into this container - health
# exporters, operators running ad-hoc queries. The image ships no chain binary,
# so this resolves the staged one at call time rather than baking in a path:
# cosmovisor's `current` symlink is what decides which version is live, and
# LAYER_HOME may be overridden.
RUN printf '%s\n' \
      '#!/bin/sh' \
      'exec "${LAYER_HOME:-/root/chain}/cosmovisor/current/bin/layerd" "$@"' \
    > /usr/local/bin/layerd \
 && chmod 0755 /usr/local/bin/layerd

ENV LAYER_HOME=/root/chain
WORKDIR /root/chain
VOLUME ["/root/chain"]

# tini reaps the processes cosmovisor spawns across an upgrade restart.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/layerctl"]
CMD ["run", "start"]
