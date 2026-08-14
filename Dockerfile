FROM golangci/golangci-lint:v2.12.2@sha256:5cceeef04e53efe1470638d4b4b4f5ceefd574955ab3941b2d9a68a8c9ad5240 AS lint
FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build
ARG BUILD_VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=lint /usr/bin/golangci-lint /usr/bin/golangci-lint

RUN golangci-lint run ./... \
    && go test ./... \
    && go vet ./... \
    && CGO_ENABLED=0 go build -tags timetzdata -trimpath -ldflags="-s -w -X main.buildVersion=$(printf '%s' "$BUILD_VERSION" | cut -c1-8)" -o /hmd . \
    && mkdir -m 1777 /scratch-tmp

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chmod=1777 /scratch-tmp /tmp
COPY --from=build /hmd /hmd
EXPOSE 8080
ENTRYPOINT ["/hmd"]
