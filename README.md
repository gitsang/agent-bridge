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

我们使用插件的形式来实现不同聊天软件的集成。

插件的职责只有：**传递消息**

负责建立通讯软件与 Connect 之间的桥梁，负责数据转换等工作。

### 接口

插件使用统一的接口

```go
type Plugin interface {
	Name() string
	Serve(ctx context.Context, handle HandleFunc) error
	Send(ctx context.Context, req *connect.Message) (*connect.Message, error)
}
```

只需要做一件事情，就是如何接收消息，转换消息，调用 HandlerFunc 处理，然后转换响应，将消息送回通讯软件。

一个简单的伪代码例子：

```go
func (p *MyPlugin) Serve(ctx context.Context, handle plugin.HandleFunc) error {
  for {
    message := ReceiveMessageFromChatApp()
    response, _ := handle(ctx, message)
    SendMessageToChatApp(response)
  }
}
```

## Contribute

## TODO List

- [ ] 支持 SO 插件
- [ ] 统一和完善 Message 命令列表
- [ ] 完善部署和使用教程
- [ ] 支持 ACP 协议对接 claude code 等 agent
