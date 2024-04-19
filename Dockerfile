# Use a small base image
FROM alpine
ARG BINARY
COPY .iscontainer /
COPY ${BINARY} /
ENTRYPOINT ["/${BINARY}"]

# Default command when running the container
CMD ["serve"]
