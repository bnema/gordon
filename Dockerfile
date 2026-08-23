# Gordon v2 production image.
# Build with BuildKit or Podman to select TARGETOS and TARGETARCH.

ARG BUILDPLATFORM
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

FROM --platform=$BUILDPLATFORM golang:1.27-alpine3.22 AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION
ARG COMMIT
ARG BUILD_DATE

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o /gordon ./main.go

FROM alpine:3.22

ARG VERSION
ARG COMMIT
ARG BUILD_DATE

RUN apk add --no-cache \
    ca-certificates \
    docker-cli \
    curl \
    wget \
    tzdata \
    && rm -rf /var/cache/apk/*

RUN adduser -D -s /bin/sh gordon

WORKDIR /app

COPY --from=builder /gordon ./gordon

RUN mkdir -p /data && chown gordon:gordon /data
COPY --chown=gordon:gordon gordon.toml.example /app/gordon.toml.example

USER gordon

EXPOSE 8088 5000

# Admin health is served on the registry/admin listener and is gated by auth in
# normal deployments, so this image does not declare a Docker healthcheck.
CMD ["./gordon", "serve"]

LABEL org.opencontainers.image.title="Gordon"
LABEL org.opencontainers.image.description="Event-driven container deployment platform"
LABEL org.opencontainers.image.source="https://github.com/bnema/gordon"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${COMMIT}"
LABEL org.opencontainers.image.created="${BUILD_DATE}"
