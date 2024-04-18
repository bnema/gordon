# Use a small base image
FROM alpine
ARG BINARY
COPY .iscontainer /
COPY gordon /
ENTRYPOINT ["/gordon"]

# Default command when running the container
CMD ["serve"]
