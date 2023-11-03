# Use a small base image
FROM alpine

COPY .iscontainer /
# Copy the compiled binary from your build context into the container
COPY gordon /

# Set the binary as the entrypoint of the container
ENTRYPOINT ["/gordon"]

# Default command when running the container
CMD ["serve"]