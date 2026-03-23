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
- UME plugin provides a webhook endpoint that strips `<at ...>...</at>` mentions, de-duplicates repeated `msgId`, and binds `sessionId` to opencode sessions in memory
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

When the OpenAI-compatible plugin receives a request without `user`, it forwards an empty session ID to `connect`, stores the returned opencode session ID in memory, and reuses it for later anonymous requests.

### Request

```bash
curl -X POST -H "Content-Type: application/json" \
  -d '{
    "model": "opencode-connect",
    "messages": [
      {"role": "user", "content": "hello world"}
    ]
  }' \
  http://127.0.0.1:8192/chat/completions
```

Add `"user": "ses_xxx"` only when you want to target an existing opencode session explicitly.

### Build & Run

```bash
cp configs/config.example.yaml configs/config.yaml
go run ./cmd/opencode-connect -c configs/config.yaml
```

### Test script

```bash
chmod +x scripts/chat-curl.sh
./scripts/chat-curl.sh "hello world"
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

  ume-bot:
    ume:
      listen: ":8194"
      # optional, defaults to https://uc.yealink.com:443/linker/robot/send
      send_url: "https://uc.yealink.com:443/linker/robot/send"
```

### UME webhook

`ume` listens for `POST /?access_token=...` with a JSON array payload such as:

```json
[
  {
    "body": "<at id=\"6943cf64f5e6479b808ce93de9c9b47c\">Opencode</at> hi",
    "msgId": 742841436585590784,
    "msgType": "text",
    "sessionId": 742105222021128192
  }
]
```

The plugin removes the `<at ...>...</at>` prefix before sending the message to `connect.Handle`, remembers the returned opencode session ID per UME `sessionId`, and ignores retries where the same `msgId` is received again for that UME session.
