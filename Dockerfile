FROM golangci/golangci-lint:v2.13.1@sha256:d371321370bf2907bd13a8f6f8baff0e0ca7438d76fdf636b281eadf7e2305e3 AS lint
FROM golang:1.27.0-bookworm@sha256:484ef6066fa69acb059fdfeda7ba2b8f7391f2ef6abc6f9b8411e669ebd56466 AS build
ARG BUILD_VERSION=dev
ARG VCS_REF=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=lint /usr/bin/golangci-lint /usr/bin/golangci-lint

RUN golangci-lint run ./... \
    && go test ./... \
    && go vet ./... \
    && CGO_ENABLED=0 go build -tags timetzdata -trimpath -ldflags="-s -w -X hmd/internal/app.buildVersion=$(printf '%s' "$BUILD_VERSION" | cut -c1-8)" -o /hmd ./cmd/hmd \
    && mkdir -m 1777 /scratch-tmp \
    && install -d -m 0700 -o 65532 -g 65532 /data/app /data/repo

FROM scratch
ARG BUILD_VERSION=dev
ARG VCS_REF=unknown
LABEL org.opencontainers.image.source="https://github.com/soollabs/holdmydocs" \
      org.opencontainers.image.revision="$VCS_REF" \
      org.opencontainers.image.version="$BUILD_VERSION" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chmod=1777 /scratch-tmp /tmp
COPY --from=build /hmd /hmd
COPY --from=build --chown=65532:65532 --chmod=0700 /data/app /data/app
COPY --from=build --chown=65532:65532 --chmod=0700 /data/repo /data/repo
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hmd"]
