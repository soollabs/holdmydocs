# Hold My Docs

Hold My Docs (HMD) is a self-hosted, Git-backed wiki for documentation, knowledge bases, and agent memory. It runs as a single Go binary and stores Markdown in an ordinary Git repository.

## Features

- Git is the source of truth: every save is a commit and can push to a remote.
- Namespaces provide per-area visibility, navigation, appearance, and page creation defaults.
- Split-pane Markdown editor with live preview, wiki-links, backlinks, search, Mermaid diagrams, attachments, history, and revert. With an operator-supplied Apache Tika Server, PDF and Office attachments also support local hybrid keyword/semantic search.
- Local accounts, OIDC, access scopes, and personal access tokens.
- MCP for authorised agents to read and write ordinary pages, plus optional namespace-filtered attachment search.
- Static export for publishing a namespace without a running HMD instance.

The HMD documentation site is itself a static export of HMD.

## Quick Start

### Docker

```sh
export HMD_ADMIN_USER=admin
export HMD_ADMIN_PASSWORD='use-a-unique-password-of-at-least-12-characters'
docker compose up -d
curl -f http://127.0.0.1:8080/_/ready
```

Visit <http://localhost:8080> and sign in with the bootstrap credentials you supplied. Remove the bootstrap environment variables after the first successful start.

The Compose example binds HMD to loopback, runs it as UID/GID 65532 with a read-only root filesystem, and limits it to 1 CPU, 512 MiB and 128 processes. Its named volumes are initialised with that ownership; recreate old root-owned volumes rather than changing their ownership in place. Uncomment `HMD_TIKA_URL` and run with `docker compose --profile documents up` to enable the internal-only, separately limited Apache Tika service. Without that variable, supported attachments remain immutable Git files but are not extracted or embedded.

### Bare Binary

```sh
go build -o hmd ./cmd/hmd
HMD_BIND=:8080 \
HMD_REPO_DIR=./data/repo \
HMD_APP_DIR=./data/app \
HMD_ADMIN_USER=admin HMD_ADMIN_PASSWORD='use-a-unique-password-of-at-least-12-characters' \
./hmd
```

To enable document search, set `HMD_TIKA_URL=https://your-tika-server:9998`. On first start, HMD downloads the pinned `BAAI/bge-small-en-v1.5` ONNX model into `<HMD_APP_DIR>/models`; each file is size- and checksum-verified before use. This applies to both containers and bare binaries, so model data is never part of the image. `HMD_DOCUMENT_SEARCH_MODEL` selects another Hugging Face model; it must provide Hugot-compatible ONNX files and 384-dimensional output. The default model is pinned and verified; a selected alternative is trusted as operator configuration. Tika owns parser isolation and OCR resource limits; keep it private and use a dedicated sandbox with parser-specific limits when processing hostile documents.

`HMD_REPO_DIR` holds the Git-backed content. Keep `HMD_APP_DIR` on local disk; it contains application state and must not be on NFS.

## Documentation

Read the [installation guide](https://docs.example.com/hmd/getting-started/install) before exposing HMD to a network. It covers TLS, secrets, storage, backup and recovery. The full documentation site also has configuration, operations, MCP, and development guides.

## Architecture

HMD keeps transport adapters separate from transport-independent application operations:

- `internal/api` — shared application operations, permissions, validation and mutation coordination. Server-rendered HTML and MCP call this package directly.
- `internal/web` — server-rendered HTML, templates, themes and assets.
- `internal/httpapi` — the browser's JSON data and mutation endpoints plus attachment upload/download. Browser JavaScript calls this surface.
- `internal/mcp` — MCP protocol handling and tools.
- `internal/app` — dependency wiring, route assembly and lifecycle.

The adapters never import one another, and application packages never import an adapter or `app`. The browser calls `/_/api/*` over HTTP; every adapter funnels through the same `api` operations, so there are no HTTP loopback calls and no parallel implementations.

Application operations enforce scopes and namespace policy independently of transport middleware. The `settings` scope implies administrator access; setup and namespace exports require it. Anonymous reads are limited to public pages and their attachments. Upload capabilities are redeemed inside `api`, which owns their one-use token, expiry, page, filename and actor.

Adapter import tests forbid direct `store`/`search` access. Neutral navigation/tag value types live in `wiki`, not in a persistence adapter.

## Development

Requires Go 1.27, Node.js (for the dependency-free JavaScript tests), and the Dockerfile-pinned `golangci-lint` v2.13.2. Docker is needed for the image check. From a clean checkout:

```sh
go test ./...
go test -race ./...
go vet ./...
golangci-lint run ./...
node --test test/ui/*.test.cjs
docker build -t hmd:check .
```

The Go suite includes a compiled-server lifecycle and CLI export check using temporary data directories. The JavaScript suite executes the production script with DOM/CodeMirror fixtures, checking saves, errors, conflicts, draft preservation and index warnings; it is not a real-browser layout test. The race suite can take several minutes because authentication tests exercise password hashing.

To check the container's CLI readiness probe without exposing a port:

```sh
docker run -d --name hmd-check \
  -e HMD_ADMIN_USER=admin -e HMD_ADMIN_PASSWORD='temporary-check-password' \
  hmd:check
docker exec hmd-check /hmd -healthcheck
docker stop hmd-check
docker rm hmd-check
```

Run the probe after the server reports that it is listening. These checks need no live content repository, Tika server, model download or OIDC provider; external-service tests use fixtures.
