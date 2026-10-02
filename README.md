# layer-docker

A Docker image that runs a Tellor Layer node from the **official upstream release
binaries**, supervised by cosmovisor, so chain upgrades need no operator action.

There is no fork of the chain source here. The image ships only `cosmovisor` and a
small Go supervisor, `layerctl`; the `layerd` binaries are downloaded at runtime
from `tellor-io/layer` releases and verified against the checksums published with
each release.

## How an upgrade happens

1. Governance passes an upgrade plan, e.g. `v6.1.8` at height `22460000`.
2. `layerctl` polls the chain for the plan, sees `v6.1.8`, downloads that release,
   checks it against the published SHA-256, and writes it to
   `cosmovisor/upgrades/v6.1.8/bin/layerd`.
3. The chain reaches the height, `layerd` halts, and cosmovisor restarts on the
   staged binary.

Nothing between step 1 and step 3 requires a human. Tellor's upgrade plan names are
the release tags, so the plan name is used directly as the directory name with no
lookup table.

## Usage

```yaml
services:
  layer:
    image: ghcr.io/cryptoriums/layer-docker:latest
    restart: unless-stopped
    environment:
      LAYER_VERSION: v6.1.8                            # version to run now
      LAYER_CHAIN_API: https://mainnet.tellorlayer.com # where to read upgrade plans
    volumes:
      - ./chain:/root/chain
    command:
      - run
      - start
      - --home=/root/chain
```

Everything after `run` is passed through to `layerd`.

## Configuration

| variable | default | meaning |
|---|---|---|
| `LAYER_HOME` | `/root/chain` | node home |
| `LAYER_VERSION` | — | version to run now; required on a fresh home |
| `LAYER_CHAIN_API` | `https://mainnet.tellorlayer.com` | comma-separated REST endpoints for reading upgrade plans |
| `LAYER_POLL` | `5m` | how often to check for a scheduled upgrade |
| `LAYER_REPO` | `tellor-io/layer` | release repository |
| `LAYER_NO_WATCH` | — | set to `1` to disable automatic staging |

List more than one endpoint in `LAYER_CHAIN_API` if you want the check to survive
your own node being down — which is exactly when an upgrade is most likely to be
missed.

## Commands

```
layerctl run [-- <layerd args>]   stage, watch for upgrades, exec cosmovisor
layerctl stage <version>          download and stage one version, then exit
```

`layerctl stage v6.1.9` is the manual escape hatch: useful to pre-stage a release
before the proposal passes, or to prepare an upgrade whose plan name is not a
release tag.

## Design notes

**Cosmovisor never downloads anything.** `daemon_allow_download_binaries` stays
`false`. Tellor's upgrade plans carry an empty `info` field, so cosmovisor has no
URL to fetch, and letting a validator execute a binary named by an on-chain message
is not a property worth having. `layerctl` does the fetching, from a fixed
repository, verified against published checksums.

**A staged upgrade is never overwritten.** Once a plan directory holds a binary,
that is the artifact the node is prepared to run at a known height. Re-staging
fails loudly rather than silently changing what will execute.

**`layerd` is on PATH inside the container**, as a shim that execs whichever
version cosmovisor currently points at. The image ships no chain binary, but
host tooling and ad-hoc operator queries reasonably expect `docker exec <ctr>
layerd query ...` to work, and silently breaking that is not worth the purity.

**Binaries are installed atomically** — written to a temp file in the destination
directory and renamed — so cosmovisor, which may be scanning at any moment, can
never observe a half-written binary.

**An upgrade whose name is not a release tag is reported, not skipped.** It is
logged as needing manual staging, because silence would be indistinguishable from
"nothing to do" — and the consequence of being wrong is a halted validator.

## Building

```sh
docker build -t layer-docker .
go test ./...
```

## Publishing

`.github/workflows/docker.yml` builds and pushes to `ghcr.io` on every push to
`main` and on `v*` tags. Tests, `go vet` and `gofmt` run first and the image is
only published if they pass, because the nodes pull it.

```
ghcr.io/cryptoriums/layer-docker:main        # tracks main
ghcr.io/cryptoriums/layer-docker:sha-<short> # pin to an exact build
ghcr.io/cryptoriums/layer-docker:v1.2.3      # release tags
ghcr.io/cryptoriums/layer-docker:latest      # newest release tag
```
