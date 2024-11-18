FROM --platform=$TARGETPLATFORM alpine:latest

COPY gordon /gordon

RUN touch /.iscontainer

ENTRYPOINT ["/gordon"]
CMD ["serve"]
