# opencode-connect
An opencode plugin for connecting opencode to chat application

## Phase 1: Chat API

This repository now includes a plugin-oriented `opencode-connect` runtime.

### Features

- Configurable opencode server `base_url` and password header
- Plugin-based integration entry (`plugins.<instance>.<type>`)
- `opencode-connect` core owns directives/commands parsing and prompt invocation
- Plugin owns chat transport adaptation and chat-session/opencode-session binding
- ChatAPI plugin provides an OpenAI-compatible `POST /chat/completions` endpoint via `Serve(handle)`
- In-memory mapping from chat `session_id` to opencode session
- Message head commands:
  - `@session:{opencode-session-id}`
  - `@model:{provider/model}` or alias from config
  - `/sessions`

### Core/Plugin contract

```go
type Plugin interface {
  Serve(ctx context.Context, handle func(context.Context, *connect.Message) (*connect.Message, error)) error
  Send(ctx context.Context, req *connect.Message) (*connect.Message, error)
}
```

`connect.Handle` is the single core entry for all plugin requests.

### Request

```bash
curl -X POST -H "Content-Type: application/json" \
  -d '{
    "model": "opencode-connect",
    "messages": [
      {"role": "user", "content": "hello world"}
    ],
    "user": "1"
  }' \
  http://127.0.0.1:8192/chat/completions
```

### Build & Run

```bash
cp configs/config.example.yaml configs/config.yaml
go run ./cmd/opencode-connect -c configs/config.yaml
```

### Test script

```bash
chmod +x scripts/chat-curl.sh
./scripts/chat-curl.sh "hello world" 1
```

### Config via env

Environment variables are supported by `configer` with prefix `OPENCODE_CONNECT_`, for example:

- `OPENCODE_CONNECT_OPENCODE_BASE_URL`
- `OPENCODE_CONNECT_OPENCODE_PASSWORD`

Plugin instances are now map-based, so plugin configuration is best kept in YAML.

### Plugin config example

```yaml
plugins:
  openai-chat:
    chatapi:
      listen: ":8192"

  webui-chat:
    chatapi:
      listen: ":8193"
```
