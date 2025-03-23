FROM --platform=$TARGETPLATFORM alpine:latest

# Install diagnostic utilities
RUN apk add --no-cache \
    curl \
    wget \
    net-tools \
    bind-tools \
    busybox-extras

COPY gordon /gordon
COPY pkg/scripts/container_diagnostic/diagnostic.sh /diagnostic.sh
RUN chmod +x /diagnostic.sh

RUN touch /.iscontainer

# Ensure we're running as root to bind to privileged ports
USER root

# Expose the ports the server needs to listen on
EXPOSE 80 443 8080

ENTRYPOINT ["/gordon"]
CMD ["serve", "--use-fallback-binding", "--proxy-debug"]
