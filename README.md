# opencode-connect

An opencode plugin for connecting opencode to chat application

## Features

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

## Plugins

## Contribute
