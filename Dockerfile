FROM scratch

COPY gordon /

EXPOSE 1323
ENTRYPOINT ["/gordon"]
CMD ["serve"]
