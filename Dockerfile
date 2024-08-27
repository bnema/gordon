# Use a minimal base image
FROM alpine
# Install ca-certificates bundle
RUN apk update && apk add ca-certificates && rm -rf /var/cache/apk/*
# Copy the binary
COPY gordon /gordon
# Copy other necessary files
COPY .iscontainer /
# Set the entrypoint
ENTRYPOINT ["/gordon"]
# Default command
CMD ["serve"]
