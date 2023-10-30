# Fetch the CA certificates from BusyBox
FROM busybox:latest as certs
RUN mkdir -p /etc/ssl/certs/ && \ 
    wget -O /etc/ssl/certs/ca-certificates.crt https://curl.se/ca/cacert-2023-08-22.pem

# Start from scratch
FROM scratch

# Copy CA certificates
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY gordon /

EXPOSE 1323
ENTRYPOINT ["/gordon"]
CMD ["serve"]
