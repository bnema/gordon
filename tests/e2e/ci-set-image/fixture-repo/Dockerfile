# e2e-demo: answers "e2e-demo service=<SERVICE> version=<VERSION>" on :8000.
# VERSION is baked at build time; SERVICE comes from the app manifest.
FROM busybox:1.36.1
ARG VERSION=dev
ENV VERSION=${VERSION}
EXPOSE 8000
CMD ["sh", "-c", "mkdir -p /www && printf 'e2e-demo service=%s version=%s\\n' \"${SERVICE:-unknown}\" \"$VERSION\" > /www/index.html && exec httpd -f -p 8000 -h /www"]
