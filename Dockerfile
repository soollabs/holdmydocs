FROM golang:1.26.5-bookworm@sha256:53eeac89074db483fdf0ab3be1df32bf6e47562263d2d0d6baa7f26acb4957dd AS build
ARG BUILD_VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN go test ./... \
    && go vet ./... \
    && CGO_ENABLED=0 go build -tags timetzdata -trimpath -ldflags="-s -w -X main.buildVersion=$(printf '%s' "$BUILD_VERSION" | cut -c1-8)" -o /hmd . \
    && mkdir -m 1777 /scratch-tmp

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chmod=1777 /scratch-tmp /tmp
COPY --from=build /hmd /hmd
EXPOSE 8080
ENTRYPOINT ["/hmd"]
