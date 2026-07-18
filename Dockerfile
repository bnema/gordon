# Gordon uses one binary for monolith, control, runtime, edge, and registry roles.
# The default target builds from source; GoReleaser selects the release target
# and injects its already-built binary for the requested platform.

FROM golang:1.26.5-alpine3.24 AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN apk add --no-cache git ca-certificates tzdata
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build \
    -trimpath \
    -ldflags='-w -s' \
    -o /out/gordon .

FROM alpine:3.24 AS runtime-base
RUN apk add --no-cache ca-certificates docker-cli curl wget tzdata \
    && adduser -D -s /bin/sh gordon \
    && mkdir -p /app /data \
    && chown -R gordon:gordon /app /data
WORKDIR /app
USER gordon
EXPOSE 8088 5000
ENTRYPOINT ["/app/gordon"]
CMD ["serve"]
LABEL org.opencontainers.image.source="https://github.com/bnema/gordon" \
      org.opencontainers.image.description="Self-hosted container deployment platform"

FROM runtime-base AS source-image
COPY --from=builder --chown=gordon:gordon /out/gordon /app/gordon
COPY --chown=gordon:gordon gordon.toml.example /app/gordon.toml.example

FROM runtime-base AS release
ARG TARGETPLATFORM
COPY --chown=gordon:gordon ${TARGETPLATFORM}/gordon /app/gordon
COPY --chown=gordon:gordon gordon.toml.example /app/gordon.toml.example

# Keep ordinary `docker build .` source-compatible while allowing GoReleaser to
# select `--target=release` for its multi-platform artifact context.
FROM source-image AS final
