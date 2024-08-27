# Start from scratch
FROM alpine:latest

ARG ARCH

# Copy the pre-built binary for the specific architecture
COPY dist/gordon-linux-${ARCH} /gordon
# Create the .iscontainer file
RUN touch /.iscontainer

# Set the entrypoint
ENTRYPOINT ["/gordon"]

# Default command
CMD ["serve"]
