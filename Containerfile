FROM alpine:latest

WORKDIR /app

COPY ./.dist/agent-bridge /usr/local/bin/agent-bridge
COPY ./configs/config.example.yaml /app/configs/config.yaml

ENTRYPOINT ["/usr/local/bin/agent-bridge"]
CMD ["-c", "/app/configs/config.yaml"]
