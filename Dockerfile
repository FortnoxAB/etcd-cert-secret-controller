FROM alpine:3.10
RUN apk add --no-cache ca-certificates
COPY etcd-cert-secret-controller /
ENTRYPOINT ["/etcd-cert-secret-controller"]
