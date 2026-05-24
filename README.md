# agent-bridge

An agent bridge for connecting AI agents to chat applications

## Plugins

我们使用插件的形式来实现不同聊天软件的集成。

使用明确的职责区分，插件的职责只有：**传递消息**，负责建立通讯软件与 Connect 之间的桥梁。

### 接口

插件使用统一的接口

```go
type Plugin interface {
	Name() string
	Serve(ctx context.Context, handle HandleFunc) error
	Send(ctx context.Context, req *bridge.Message) (*bridge.Message, error)
}
```

只需要做一件事情，就是如何接收消息，转换消息，调用 HandlerFunc 处理，然后将 reply 回调收到的消息送回通讯软件。

`HandleFunc` 签名如下：

```go
type HandleFunc func(ctx context.Context, req *bridge.Message, reply bridge.ReplyFunc) error
```

`reply` 是一个回调，agent 每产出一条回复就会调用一次。对于支持流式输出的通讯软件，插件可以在 `reply` 被调用时立即发送；对于只需要最终结果的场景（如 HTTP 同步接口），可以在回调里暂存最后一条消息，等 handle 返回后再使用。

一个简单的伪代码例子：

```go
func (p *MyPlugin) Serve(ctx context.Context, handle plugin.HandleFunc) error {
  for {
    message := ReceiveMessageFromChatApp()
    err := handle(ctx, message, func(reply *bridge.Message) error {
      SendMessageToChatApp(reply)
      return nil
    })
    if err != nil {
      SendErrorToChatApp(err)
    }
  }
}
```

Send 接口当前暂时没有使用，是为了后续的 Heartbeat/Schedule 等主动发送场景预留的通道。

### Supports

- [x] OpenAI-compatible Chat Completions API
- [x] Mattermost
- [ ] Slack

### Mattermost

Mattermost can call agent-bridge through an outgoing webhook or slash command endpoint.

```yaml
plugins:
  mattermost:
    mattermost:
      listen: ":24370"
      token: "mattermost-token"
      response_url_hosts:
        - "mattermost.example.com"
```

Configure the Mattermost integration URL to the plugin endpoint. The plugin accepts
Mattermost form payloads, validates either the form `token` or
`Authorization: Token <token>`, maps `team_id`, `channel_id`, and `user_id` to
the chat session, then sends agent replies back as Mattermost JSON responses.
When Mattermost provides `response_url`, replies are posted to that URL so
multiple streaming replies can be delivered. Add the Mattermost `response_url`
host to `response_url_hosts` so the plugin only calls approved callback hosts.

## Slash commands:

- `/new [--model <provider/model|model>] [--agent <name>] [--directory <path>] [--title <title>]`
- `/session attach <agent-session-id>`
- `/session detach`
- `/session current`
- `/session list [--directory <path>]`
- `/model set <provider/model|model>`
- `/model list`
- `/agent set <name>`
- `/agent list`
- `/directory set <path>`
- `/permission <once|always|reject> [id|index]`
- `/question [id|index] <answer...>`
- `/question reject [id|index]`
- `/help [new|session|model|agent|directory|permission|question]`


## Codex driver

可以直接使用 Codex app-server 作为 agent driver：

```yaml
agent:
  driver: codex
  codex:
    command: "codex"
    args: ["app-server", "--listen", "stdio://"]
    timeout: 30m
    initialize_timeout: 15s
```

示例配置见 `configs/config.codex.example.yaml` 和 `configs/config.codex.develop.yaml`。

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
- [x] 支持 agent 多轮响应（通过 reply 回调逐条推送）
- [ ] 支持 SO 插件
- [ ] 完善部署和使用教程
- [ ] 支持 ACP 协议对接 claude code 等 agent
- [x] 支持 Codex agent driver（通过 `codex app-server`）
- [ ] 支持其他 agent 接入
