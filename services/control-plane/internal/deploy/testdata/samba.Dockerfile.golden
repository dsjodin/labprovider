FROM docker.io/library/alpine:3.22
RUN apk add --no-cache samba
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
