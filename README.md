# Hold My Docs

Hold My Docs (HMD) is a self-hosted, Git-backed wiki for documentation,
knowledge bases, and agent memory. It runs as a single Go binary and stores
Markdown in an ordinary Git repository.

## Features

- Git is the source of truth: every save is a commit and can push to a remote.
- Namespaces provide per-area visibility, navigation, appearance, and page
  creation defaults.
- Split-pane Markdown editor with live preview, wiki-links, backlinks, search,
  Mermaid diagrams, attachments, history, and revert.
- Local accounts, OIDC, access scopes, and personal access tokens.
- MCP for authorised agents to read and write ordinary pages.
- Static export for publishing a namespace without a running HMD instance.

The HMD documentation site is itself a static export of HMD.

## Quick Start

### Docker

```sh
echo "your_git_pat_here" > git_token.txt
docker-compose up
```

Visit <http://localhost:8080> and sign in with `admin` / `change-me`. Change
these example credentials before exposing the service.

### Bare Binary

```sh
go build -o hmd .
HMD_BIND=:8080 \
HMD_REPO_DIR=./data/repo \
HMD_APP_DIR=./data/app \
HMD_ADMIN_USER=admin HMD_ADMIN_PASSWORD=change-me \
./hmd
```

`HMD_REPO_DIR` holds the Git-backed content. Keep `HMD_APP_DIR` on local disk;
it contains application state and must not be on NFS.

## Documentation

Full installation, configuration, operations, writing, MCP, and development
guides are published with the HMD documentation site.

## Development

Requires Go 1.26.5.

```sh
go test ./...
go vet ./...
```
