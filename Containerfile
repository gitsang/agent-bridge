FROM alpine:latest

WORKDIR /app

COPY ./.dist/opencode-connect /usr/local/bin/opencode-connect
COPY ./configs/config.example.yaml /app/configs/config.yaml

ENTRYPOINT ["/usr/local/bin/opencode-connect"]
CMD ["-c", "/app/configs/config.yaml"]
