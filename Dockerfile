FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG BUILT_BY=docker
ARG TARGETOS=linux
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-$(go env GOARCH)} go build \
  -trimpath \
  -ldflags "-s -w -X github.com/nicotsx/microhook/internal/buildinfo.Version=${VERSION} -X github.com/nicotsx/microhook/internal/buildinfo.Commit=${COMMIT} -X github.com/nicotsx/microhook/internal/buildinfo.BuildTime=${BUILD_TIME} -X github.com/nicotsx/microhook/internal/buildinfo.BuiltBy=${BUILT_BY}" \
  -o /out/microhook ./cmd/microhook

FROM alpine:3.22

RUN addgroup -S microhook && adduser -S -G microhook -h /var/lib/microhook microhook

COPY --from=build /out/microhook /usr/local/bin/microhook

RUN mkdir -p /etc/microhook /var/lib/microhook && chown -R microhook:microhook /etc/microhook /var/lib/microhook

USER microhook
WORKDIR /var/lib/microhook

EXPOSE 9464
VOLUME ["/etc/microhook", "/var/lib/microhook"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD wget -qO- http://127.0.0.1:9464/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/microhook"]
CMD ["serve"]
