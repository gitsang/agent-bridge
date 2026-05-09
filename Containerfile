FROM alpine:latest

ARG DIST_PATH=dist/agent-bridge/linux/amd64/bin/agent-bridge

WORKDIR /app

COPY ./${DIST_PATH} /usr/local/bin/agent-bridge
COPY ./configs/config.example.yaml /app/configs/config.yaml

ENTRYPOINT ["/usr/local/bin/agent-bridge"]
CMD ["-c", "/app/configs/config.yaml"]
