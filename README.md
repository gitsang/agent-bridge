# opencode-connect

An opencode plugin for connecting opencode to chat application

## Plugins

我们使用插件的形式来实现不同聊天软件的集成。

使用明确的职责区分，插件的职责只有：**传递消息**，负责建立通讯软件与 Connect 之间的桥梁。

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

Send 接口当前暂时没有使用，是为了后续的 Heartbeat/Schedule 以及多轮响应预留的主动发送通道。

### Supports

- [x] OpenAI-compatible Chat Completions API
- [ ] Mattermost
- [ ] Slack

## Slash commands:

- `/new [--model <provider/model|model>] [--agent <name>] [--work-dir <path>] [--title <title>]`
- `/session attach <opencode-session-id>`
- `/session detach`
- `/session current`
- `/session list [--work-dir <path>]`
- `/model set <provider/model|model>`
- `/model list`
- `/agent set <name>`
- `/agent list`
- `/workdir set <path>`
- `/help [new|session|model|agent|workdir]`

## Conversation store

默认使用内存会话存储，进程重启后会清空。

如果需要持久化 `/session attach` 绑定，可在配置中启用文件存储：

```yaml
conversation_store:
  type: "file"
  file_path: "data/conversation_store.json"
  ttl: 24h
  max_items: 1024
```

## Contribute

## TODO List

- [x] 支持 Message 命令列表
- [ ] 支持 SO 插件
- [ ] 完善部署和使用教程
- [ ] 支持 ACP 协议对接 claude code 等 agent
