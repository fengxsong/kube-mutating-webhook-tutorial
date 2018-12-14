FROM alpine:latest

ADD mutating-webhook-demo /mutating-webhook-demo
ENTRYPOINT ["/mutating-webhook-demo"]