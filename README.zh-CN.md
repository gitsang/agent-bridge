<!-- MarkdownTitleNumber auto -->

# Agent Bridge

<p align="center">
  <a href="./README.md">English</a> | <a href="./README.zh-CN.md">中文</a>
</p>

一个连接 AI 代理与聊天应用的桥梁，让你可以通过 Mattermost、OpenAI 兼容接口等聊天软件与 AI 编程助手（如 Claude Code、Codex、OpenCode）进行交互。

在你的手机上，继续电脑上未完成的工作。

## 1. 核心思想

### 1.1 设计理念

Agent Bridge 的核心设计思想是**分离关注点**和**统一抽象**：

1. **平台与代理分离**：聊天平台只负责消息的收发，AI 代理只负责代码生成和交互，两者通过 Agent Bridge 进行连接
2. **统一接口设计**：所有聊天平台实现相同的 `Platform` 接口，所有 AI 代理实现相同的 `Agent` 接口，实现即插即用
3. **流式响应支持**：支持流式输出，让用户可以实时看到 AI 代理的思考过程和生成结果
4. **会话状态管理**：维护聊天会话与 AI 代理会话的绑定关系，支持多轮对话和会话切换

### 1.2 回调驱动的生命周期管理

Agent Bridge 采用**回调驱动**而非事件驱动的架构。这意味着：

- **Platform 只需调用一次 `HandleFunc`**：不需要管理事件 channel 的生命周期，不需要持久协程监听事件，不需要处理 channel 关闭
- **Agent 直接调用回调函数**：Agent 要回复时直接调用 `reply` 回调，Platform 不需要主动轮询或等待
- **职责分离更纯粹**：Platform 只负责收发消息，不需要理解 Agent 的内部状态和生命周期

### 1.3 架构概览

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   聊天应用      │    │  Agent Bridge   │    │    AI 代理      │
│  (Mattermost,   │───►│                 │───►│  (Claude Code,  │
│   OpenAI API,   │    │  - Platform 接口│    │   Codex,        │
│   etc.)         │◄─┐ │  - Agent 接口   │    │   OpenCode)     │
└─────────────────┘  │ │  - Bridge 核心  │    └─────────────────┘
                     │ └─────────────────┘            │
                     │                                │
                     └────── reply callback ◄─────────┘
```

**消息流向**：

1. 用户在聊天应用中发送消息
2. Platform 接收消息并转换为统一格式
3. Bridge 处理消息（解析命令、管理会话）
4. Agent 接收 prompt 并调用 AI 代理
5. AI 代理返回结果，通过 reply 回调逐条推送
6. Platform 将结果发送回聊天应用

## 2. 核心概念

### 2.1 Platform（平台）

Platform 是聊天应用的抽象层，负责：

- 接收来自聊天应用的消息
- 将消息转换为统一的 `bridge.Message` 格式
- 调用 `HandleFunc` 处理消息
- 将 AI 代理的回复发送回聊天应用

```go
type Platform interface {
    Name() string
    Serve(ctx context.Context, handle HandleFunc) error
    Send(ctx context.Context, req *bridge.Message) (*bridge.Message, error)
}
```

**HandleFunc 签名**：

```go
type HandleFunc func(ctx context.Context, req *bridge.Message, reply bridge.ReplyFunc) error
```

`reply` 是一个回调函数，AI 代理每产出一条回复就会调用一次。对于支持流式输出的聊天平台，可以在 `reply` 被调用时立即发送；对于只需要最终结果的场景，可以在回调里暂存消息，等 handle 返回后再使用。

### 2.2 Agent（代理）

Agent 是 AI 代理的抽象层，负责：

- 管理 AI 代理会话
- 发送 prompt 并获取响应
- 处理权限请求和用户问题
- 获取会话历史和模型信息

```go
type Agent interface {
    // 模型管理
    ListModels(ctx context.Context, directory string) ([]types.ModelInfo, error)
    ResolveModel(ctx context.Context, spec, directory string) (types.ModelRef, error)

    // 代理管理
    ListAgents(ctx context.Context, directory string) ([]types.AgentInfo, error)

    // 会话管理
    ListSessions(ctx context.Context, directory string) ([]types.Session, error)
    CreateSession(ctx context.Context, request types.CreateSessionRequest) (*types.Session, error)
    GetSession(ctx context.Context, sessionID string) (*types.Session, error)

    // 消息交互
    Prompt(ctx context.Context, sessionID string, prompt string, opts ...types.PromptOptionFunc) (*types.PromptHandle, error)
    PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64, output types.MessageOutputOptions) ([]*types.Message, error)

    // 权限和问题处理
    ListPendingPermissions(ctx context.Context, sessionID string) ([]types.PermissionRequest, error)
    ReplyPermission(ctx context.Context, sessionID string, requestID string, reply types.PermissionReply) error
    ListPendingQuestions(ctx context.Context, sessionID string) ([]types.QuestionRequest, error)
    ReplyQuestion(ctx context.Context, sessionID string, requestID string, answers [][]string) error
}
```

### 2.3 Bridge（桥梁）

Bridge 是核心组件，负责：

- 解析用户输入（普通消息或斜杠命令）
- 管理会话绑定关系
- 处理斜杠命令（`/new`、`/session`、`/model` 等）
- 协调 Platform 和 Agent 的交互

## 3. 快速开始

### 3.1 安装

```bash
# 从源码编译
git clone https://github.com/gitsang/agent-bridge.git
cd agent-bridge
go build -o agent-bridge ./cmd/agent-bridge

# 或使用 go install
go install github.com/gitsang/agent-bridge/cmd/agent-bridge@latest
```

### 3.2 配置

创建配置文件 `config.yaml`：

```yaml
# 日志配置
log:
  handlers:
    default: "default"
  providers:
    default:
      - format: "json"
        level: "info"
        output:
          stdout:
            enable: true

# 平台配置
platforms:
  # OpenAI 兼容 API（支持任何 OpenAI SDK 客户端）
  openai-compatible:
    openai-compatible:
      listen: ":24368"

  # Mattermost 集成
  mattermost:
    mattermost:
      mode: "websocket" # webhook 或 websocket
      websocket:
        server_url: "https://mattermost.example.com"
        ws_url: "wss://mattermost.example.com"
        access_token: "your-bot-token"

# AI 代理配置
agent:
  driver: claude # 支持: opencode, codex, claude
  claude:
    command: "claude"
    args:
      - "-p"
      - "--output-format"
      - "stream-json"
      - "--verbose"
    timeout: 30m

# 会话存储配置
conversation:
  store:
    type: "sqlite" # memory, file, sqlite
    path: "data/conversation.db"
    ttl: 24h
    max_items: 1024
```

### 3.3 启动

```bash
# 使用配置文件启动
./agent-bridge --config config.yaml

# 或使用环境变量
export ANTHROPIC_API_KEY="your-api-key"
./agent-bridge --config config.yaml
```

### 3.4 测试

使用 curl 测试 OpenAI 兼容 API：

```bash
curl http://localhost:24368/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "messages": [
      {"role": "user", "content": "Hello, how are you?"}
    ]
  }'
```

## 4. 配置说明

### 4.1 平台配置

#### 4.1.1 OpenAI 兼容 API

提供标准的 OpenAI Chat Completions API 接口，支持任何 OpenAI SDK 客户端。

```yaml
platforms:
  openai-compatible:
    openai-compatible:
      listen: ":24368" # 监听地址
```

#### 4.1.2 Mattermost（WebSocket）

适用于 Mattermost 无法直接访问 Agent Bridge 的场景（如内网环境），通过 WebSocket 主动连接 Mattermost。

```yaml
platforms:
  mattermost:
    mattermost:
      mode: "websocket"
      command_prefix: "!" # 命令前缀，默认 "/"
      websocket:
        server_url: "https://mattermost.example.com"
        ws_url: "wss://mattermost.example.com"
        access_token: "bot-access-token"
```

#### 4.1.3 Mattermost（Webhook）

通过 Mattermost 的 Outgoing Webhook 或 Slash Command 接收消息。

```yaml
platforms:
  mattermost:
    mattermost:
      mode: "webhook"
      webhook:
        listen: ":24370"
        token: "mattermost-token"
        response_url_hosts:
          - "mattermost.example.com"
```

### 4.2 AI 代理配置

#### 4.2.1 Claude Code

使用 Claude Code CLI 的非交互模式作为 AI 代理。

```yaml
agent:
  driver: claude
  claude:
    command: "claude"
    args:
      - "-p"
      - "--output-format"
      - "stream-json"
      - "--verbose"
    timeout: 30m
```

**注意事项**：

- 使用 `--bare` 参数可跳过 Claude Code 的 hooks、plugins、MCP 等功能
- 通过 `ANTHROPIC_API_KEY` 环境变量提供认证
- 当前版本不支持 `/permission` 和 `/question` 命令

#### 4.2.2 Codex

使用 Codex app-server 作为 AI 代理。

```yaml
agent:
  driver: codex
  codex:
    command: "codex"
    args:
      - "app-server"
      - "--listen"
      - "stdio://"
    timeout: 30m
    initialize_timeout: 15s
```

#### 4.2.3 OpenCode

使用 OpenCode 作为 AI 代理（默认驱动）。

```yaml
agent:
  driver: opencode
  opencode:
    base_url: "http://127.0.0.1:4096"
    timeout: 10m
    db_path: "/root/.local/share/opencode/opencode.db"
```

### 4.3 消息输出配置

可以配置 AI 代理返回的消息类型：

```yaml
agent:
  message_output:
    include:
      - answer # 回答内容
      - reasoning # 思考过程
      - action # 工具调用
      - artifact # 代码变更
      - diagnostic # 诊断信息
```

### 4.4 会话存储配置

```yaml
conversation:
  store:
    type: "sqlite" # memory, file, sqlite
    path: "data/conversation.db" # 文件路径（file 和 sqlite 类型）
    ttl: 24h # 会话过期时间
    max_items: 1024 # 最大会话数
  message:
    include_user_identity: false # 是否在消息中包含用户身份
```

### 4.5 日志调试

启用调试日志：

```yaml
log:
  providers:
    default:
      - format: "json"
        level: "debug"
        verbosity: 3
        output:
          stdout:
            enable: true
```

## 5. 命令参考

Agent Bridge 支持以下斜杠命令：

### 5.1 会话管理

```bash
# 创建新会话
/new [--model <provider/model|model>] [--agent <name>] [--directory <path>] [--title <title>]

# 会话操作
/session attach <agent-session-id>  # 绑定到现有会话
/session detach                     # 解绑当前会话
/session current                    # 显示当前会话信息
/session list [--directory <path>]  # 列出会话
```

### 5.2 模型和代理

```bash
# 模型管理
/model set <provider/model|model>  # 设置默认模型
/model list                        # 列出可用模型

# 代理管理
/agent set <name>  # 设置默认代理
/agent list        # 列出可用代理
```

### 5.3 工作目录

```bash
# 设置工作目录
/directory set <path>
```

### 5.4 权限和问题

```bash
# 权限请求
/permission <once|always|reject> [id|index]

# 问题回答
/question [id|index] <answer...>
/question reject [id|index]
```

### 5.5 帮助

```bash
/help [new|session|model|agent|directory|permission|question]
```

## 6. TODO List

### 6.1 平台支持

- [x] OpenAI 兼容 API（支持任何 OpenAI SDK 客户端）
- [x] Mattermost（WebSocket 模式）
- [x] Mattermost（Webhook 模式）
- [ ] Slack（计划中）

### 6.2 AI 代理支持

- [x] OpenCode（默认）
- [x] Claude Code
- [x] Codex
- [ ] 其他 ACP 兼容代理（计划中）

### 6.3 其他功能

- [x] 支持 Message 命令列表
- [x] 支持 agent 多轮响应（通过 reply 回调逐条推送）
- [ ] 支持 SO/gRPC 插件
- [ ] 支持 Capability 声明
- [ ] Agent 不要声明轮询接口，对于不支持事件推送的，也需要用轮询封装成事件推送
- [ ] 改为事件回调而不是 Message 回调

## 7. 贡献指南

欢迎贡献代码、报告问题或提出改进建议！

1. Fork 项目
2. 创建功能分支：`git checkout -b feat/your-feature`
3. 提交更改：`git commit -m 'feat: Add some feature'`
4. 推送分支：`git push origin feat/your-feature`
5. 创建 Pull Request

## 8. 许可证

本项目采用 MIT 许可证 - 详见 [LICENSE](LICENSE) 文件。
