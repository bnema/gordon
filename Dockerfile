# Gordon uses one binary for monolith, control, runtime, edge, and registry roles.
# The default target builds from source; GoReleaser selects the release target
# and injects its already-built binary for the requested platform.

# golang:1.26.5-alpine3.24, verified 2026-03-18
FROM golang:1.26.5-alpine3.24@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder
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

# alpine:3.24, verified 2026-03-18
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS runtime-base
RUN apk add --no-cache ca-certificates docker-cli curl wget tzdata pass gnupg \
    && adduser -D -s /bin/sh gordon \
    && mkdir -p /app /data /var/lib/gordon/secrets \
    && chown -R gordon:gordon /app /data /var/lib/gordon
# Keep runtime data/config discovery separate from the binary. Viper searches
# the working directory for `gordon`, so /app would mistake /app/gordon for a
# configuration file when no explicit config is mounted.
WORKDIR /data
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
