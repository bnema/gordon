FROM alpine

COPY gordon /

ENTRYPOINT ["/gordon"]
CMD ["serve"]
