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
HMD_ADMIN_USER=admin HMD_ADMIN_PASSWORD='choose-a-strong-password' docker compose up
```

Visit <http://localhost:8080> and sign in with the bootstrap credentials you supplied. Remove the bootstrap environment variables after the first successful start.

The Compose example includes an internal Apache Tika 3.x service under the `documents` profile. Uncomment `HMD_TIKA_URL` and run with `docker compose --profile documents up` to enable it; without that variable, supported attachments remain immutable Git files but are not extracted or embedded.

### Bare Binary

```sh
go build -o hmd .
HMD_BIND=:8080 \
HMD_REPO_DIR=./data/repo \
HMD_APP_DIR=./data/app \
HMD_ADMIN_USER=admin HMD_ADMIN_PASSWORD='choose-a-strong-password' \
./hmd
```

To enable document search, set `HMD_TIKA_URL=https://your-tika-server:9998`. On first start, HMD downloads the pinned `BAAI/bge-small-en-v1.5` ONNX model into `<HMD_APP_DIR>/models`; each file is size- and checksum-verified before use. This applies to both containers and bare binaries, so model data is never part of the image. `HMD_DOCUMENT_SEARCH_MODEL` selects another Hugging Face model; it must provide Hugot-compatible ONNX files and 384-dimensional output. The default model is pinned and verified; a selected alternative is trusted as operator configuration. Tika owns parser isolation and OCR resource limits; keep it private and apply the server-side limits recommended for untrusted documents.

`HMD_REPO_DIR` holds the Git-backed content. Keep `HMD_APP_DIR` on local disk; it contains application state and must not be on NFS.

## Documentation

Full installation, configuration, operations, writing, MCP, and development guides are published with the HMD documentation site.

## Development

Requires Go 1.26.6.

```sh
go test ./...
go vet ./...
```
