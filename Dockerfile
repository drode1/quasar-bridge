FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates tzdata
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath \
    -ldflags="-s -w -X github.com/skrashevich/quasar-bridge/internal/update.Version=$VERSION" \
    -o /quasar-bridge ./cmd/quasar-bridge

FROM alpine:3.23
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /quasar-bridge /app/quasar-bridge
WORKDIR /app
ENTRYPOINT ["/app/quasar-bridge"]
CMD ["-config", "/config/config.json"]
