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
- [ ] Mattermost
- [ ] Slack

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

## Claude Code driver

可以使用 Claude Code CLI 的非交互 `stream-json` 输出作为 agent driver：

```yaml
agent:
  driver: claude
  claude:
    command: "claude"
    args: ["--bare", "-p", "--output-format", "stream-json", "--verbose"]
    timeout: 30m
```

Claude driver 每次 prompt 启动一次 `claude -p` 进程，并通过 `--session-id` / `--resume` 维持 Claude Code 本地会话连续性。`--bare` 是 Claude Code 官方推荐的脚本模式，会跳过 hooks、plugins、MCP、自动记忆和 keychain 读取；如果使用该模式，请通过 `ANTHROPIC_API_KEY` 或 `--settings` 中的 `apiKeyHelper` 提供认证。若需要沿用本机 Claude Code 登录态，可从 `args` 中移除 `--bare`。

由于 Claude CLI 的 `-p` 参数需要把 prompt 作为进程参数传入，运行期间本机其他同权限进程可能通过 `ps` 或 `/proc` 看到 prompt 内容。不要把密钥放进 prompt；也不建议把 `ANTHROPIC_API_KEY` 等密钥写进配置文件的 `env` 字段，优先使用进程环境变量或 Claude Code settings 中的 `apiKeyHelper`。

当前 Claude Code CLI 没有 OpenCode/Codex 那种可枚举、可回复的 mid-turn 权限/问题队列，所以 Claude driver 的等价范围限于 prompt、session/resume 和流式结果输出；`/permission` 和 `/question` 不会产生待处理项。需要自动授权工具时请通过 `args` 配置 Claude Code 的 `--allowedTools` 或 `--permission-mode`。`/session list` 只列出本进程创建过的 Claude sessions；如果已有 Claude Code session id，可以用 `/session attach <id>` 绑定并通过 `--resume` 继续。

示例配置见 `configs/config.claude.example.yaml`。

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
- [x] 支持 Claude Code agent driver（通过 `claude -p --output-format stream-json`）
- [x] 支持 Codex agent driver（通过 `codex app-server`）
- [ ] 支持其他 agent 接入
