FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0 AS build
ARG TARGETOS
ARG TARGETARCH
ARG BUILD_VERSION=dev
ARG VCS_REF=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags timetzdata -trimpath -ldflags="-s -w -X hmd/internal/app.buildVersion=$BUILD_VERSION" -o /hmd ./cmd/hmd \
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
COPY --from=build --chown=65532:65532 --chmod=0700 /scratch-tmp /tmp
COPY --from=build /hmd /hmd
COPY --from=build --chown=65532:65532 --chmod=0700 /data/app /data/app
COPY --from=build --chown=65532:65532 --chmod=0700 /data/repo /data/repo
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hmd"]
