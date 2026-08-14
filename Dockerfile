FROM golang:1.26.5-bookworm@sha256:53eeac89074db483fdf0ab3be1df32bf6e47562263d2d0d6baa7f26acb4957dd AS build
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
