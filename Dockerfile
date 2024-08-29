FROM alpine:latest

COPY gordon /gordon

RUN touch /.iscontainer

ENTRYPOINT ["/gordon"]
CMD ["serve"]
