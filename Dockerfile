# Use a small base image
FROM alpine
ARG BINARY
COPY .iscontainer /
COPY bin/gordon /
ENTRYPOINT ["/gordon"]

# Default command when running the container
CMD ["serve"]
