FROM golangci/golangci-lint:v2.13.0@sha256:8bd3a16a42f03207464b9f7cf3f9e8d3549d976ace271e583cad11aa11218974 AS lint
FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build
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
