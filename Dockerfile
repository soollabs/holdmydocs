FROM golang:1.26.5-bookworm@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599 AS build
ARG BUILD_VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN go test ./... \
    && go vet ./... \
    && go build -trimpath -ldflags="-s -w -X main.buildVersion=$(printf '%s' "$BUILD_VERSION" | cut -c1-8)" -o /hmd .

FROM debian:bookworm-slim@sha256:abd67ffcfa541b485a3dff59865ab629aa048a6c613e639d36e7456b0b229241
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /hmd /hmd
EXPOSE 8080
ENTRYPOINT ["/hmd"]
