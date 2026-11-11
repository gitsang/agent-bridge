# The binary is built with CGO_ENABLED=1 because the sqlite3 driver needs cgo,
# which makes it glibc-linked. The runtime image must therefore provide glibc:
# on Alpine (musl) the binary fails with "fcntl64: symbol not found".
FROM debian:bookworm-slim

ARG DIST_PATH=dist/agent-bridge/linux/amd64/bin/agent-bridge

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY ./${DIST_PATH} /usr/local/bin/agent-bridge
COPY ./configs/config.example.yaml /app/configs/config.yaml

ENTRYPOINT ["/usr/local/bin/agent-bridge"]
CMD ["-c", "/app/configs/config.yaml"]
