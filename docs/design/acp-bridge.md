# ACP 对接 Claude Code 与 Codex 设计文档

日期：2026-05-19  
状态：设计草案，等待实现  
范围：`agent-bridge` 作为聊天入口，通过 ACP 对接 Claude Code、Codex，以及未来兼容 ACP 的 coding agent。

## 1. 背景与目标

当前 `agent-bridge` 已经把聊天插件与 agent 执行层分开：

- 聊天入口通过 `internal/plugin.Plugin` 接收消息。
- `internal/bridge.AgentBridge` 负责 slash command、会话绑定、模型/agent/directory 默认值、权限和问题交互。
- `internal/agent.Client` 是唯一 agent 抽象，目前主要由 `internal/agent/opencode` 实现。

README TODO 已列出“支持 ACP 协议对接 claude code 等 agent”。本设计把 ACP 接入点放在 `internal/agent.Client` 层，使现有 OpenAI-compatible、UME 等插件无需理解 ACP。

目标：

1. 新增 `agent.driver: acp`，让 `agent-bridge` 能启动并连接任意 ACP Agent 子进程。
2. 提供 Claude Code shim，把 Claude Code CLI 的 stream-json 输出包装为 ACP Agent。
3. 提供 Codex shim，把 Codex app-server 协议包装为 ACP Agent。
4. 保留现有聊天命令和会话模型，避免重写 `internal/bridge`。
5. 第一版以文本 prompt、文本输出、session、permission/question、cancel 为核心；rich diff、图片、多媒体和完整 IDE 文件操作留到后续阶段。

非目标：

- 第一版不把 `agent-bridge` 暴露成 ACP server 给外部 IDE 直接连接。
- 第一版不实现完整 ACP filesystem/terminal client-side 方法。
- 第一版不改变 OpenAI-compatible 插件 API。
- 第一版不要求真实 Claude/Codex 在 CI 中可用。

## 2. 外部协议依据

ACP 官方文档定义了 Agent 和 Client 之间基于 JSON-RPC 2.0 的双向通信。典型流程为：`initialize`、可选 `authenticate`、`session/new` 或 `session/load`、`session/prompt`、`session/update`、必要时 `session/cancel`。ACP stdio transport 使用 newline-delimited JSON，Agent stdout 不能混入非协议内容。

Claude Code CLI 支持非交互 print 模式和 stream-json：`claude -p --output-format stream-json`，也支持 `--input-format stream-json`、`--resume`、`--session-id`、permission mode、tool allow/deny 等参数。因此 Claude Code 更适合作为 shim 的后端命令，而不是被假设为原生 ACP Agent。

Codex CLI 提供 `codex app-server`，该 app server 是 Codex 为 IDE/GUI 等 rich interface 暴露的 JSON-RPC 入口，支持 stdio / websocket transport。它不是 ACP，但与 ACP 一样采用长驻双向协议，适合用 adapter 翻译。

参考：

- ACP Overview: https://agentclientprotocol.com/protocol/overview
- ACP Initialization: https://agentclientprotocol.com/protocol/initialization
- ACP Session Setup: https://agentclientprotocol.com/protocol/session-setup
- ACP Prompt Turn: https://agentclientprotocol.com/protocol/prompt-turn
- ACP Transports: https://agentclientprotocol.com/protocol/transports
- Claude Code CLI usage: https://code.claude.com/docs/en/cli-usage
- Codex app-server README: https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md

## 3. 设计原则

1. **ACP 是 agent driver，不是 chat plugin。** 聊天插件只负责传递消息；ACP 属于 agent 执行协议。
2. **Shared Core + Typed Adapter。** 通用 ACP JSON-RPC/client/session 逻辑共用；Claude/Codex 差异放到独立 shim。
3. **先收敛 MVP。** 先支持文本 turn、session、cancel、permission/question；复杂 UI 能力后续增加。
4. **行为兼容现有 bridge。** `/new`、`/session`、`/model`、`/agent`、`/permission`、`/question` 仍通过 `agent.Client` 工作。
5. **真实后端可选，测试依赖 fake。** 单元测试用 fake ACP process / fake CLI stream，避免 CI 依赖账号、网络和模型额度。
6. **stdout 严格协议化。** ACP 子进程 stdout 只放 JSON-RPC；日志写 stderr 或文件。

## 4. 总体架构

```text
Chat App / OpenAI-compatible / UME
        │
        ▼
internal/plugin.Plugin
        │ bridge.Message
        ▼
internal/bridge.AgentBridge
        │ agent.Client
        ▼
internal/agent/acp.Client
        │ JSON-RPC over stdio
        ▼
ACP Agent process
        ├── agent-bridge acp-agent claude-code ── claude CLI
        └── agent-bridge acp-agent codex       ── codex app-server
```

新增两类组件：

1. **ACP client driver**：`internal/agent/acp`，运行在当前 `agent-bridge` 进程内，对上实现 `agent.Client`，对下连接 ACP Agent 子进程。
2. **ACP shim servers**：`agent-bridge acp-agent <backend>`，作为 ACP Agent 对接具体后端。

这样后续如果出现原生 ACP Agent，只需配置：

```yaml
agent:
  driver: acp
  acp:
    command: "some-native-acp-agent"
    args: []
```

## 5. 模块划分

### 5.1 `internal/agent/acp`

职责：把 ACP Agent 映射到现有 `agent.Client`。

建议文件：

```text
internal/agent/acp/
  client.go        # 实现 agent.Client
  config.go        # driver 配置和默认值
  jsonrpc.go       # stdio JSON-RPC transport、id 分配、请求/通知分发
  types.go         # ACP 最小类型集
  session_store.go # bridge session id 与 ACP session id 元数据
  interactions.go  # permission/question pending queue
  errors.go        # JSON-RPC / ACP 错误映射
```

核心职责：

- 启动 ACP 子进程并完成 `initialize`。
- 管理 JSON-RPC request/response correlation。
- 把 `session/new`、`session/prompt`、`session/cancel` 映射到 `agent.Client`。
- 接收 `session/update` 通知，聚合 assistant 文本、reasoning、tool/action 信息。
- 接收 permission/question 类型事件，写入 pending queue。
- 在 `ReplyPermission` / `ReplyQuestion` 时调用对应 ACP response 方法或 shim 自定义方法。
- context cancel / timeout 时发送 `session/cancel`，再做进程清理。

### 5.2 `internal/agent/acpshim/claude`

职责：让 Claude Code CLI 表现为 ACP Agent。

推荐策略：

- `session/new`：生成本地 ACP session id，并保存 cwd、model、permission mode、Claude session id/name。
- `session/prompt`：调用 `claude -p --output-format stream-json`。
- 多轮：优先用 `--resume <session>` 或 `--session-id <uuid>` 保持 Claude 侧上下文；如果不可用，则由 shim 保存 transcript 并在下一轮提供摘要/历史。
- 输出：解析 stream-json，将 assistant message chunk 聚合后发 ACP `session/update`，结束后返回 stop reason。
- 权限：第一版先提供安全配置：默认不绕过权限；可配置 `permission_mode`、`allowed_tools`、`disallowed_tools`。完整 interactive permission prompt 可作为第二阶段。

### 5.3 `internal/agent/acpshim/codex`

职责：让 Codex app-server 表现为 ACP Agent。

推荐策略：

- shim 启动 `codex app-server --listen stdio://` 或连接现有 app-server endpoint。
- `session/new` 映射到 Codex thread 创建。
- `session/prompt` 映射到 Codex turn 创建 / submit。
- Codex item/event 映射为 ACP `session/update`：assistant 文本、reasoning、tool call、approval request、error。
- cancel 映射到 Codex turn cancel / interrupt。
- 权限/approval 使用 Codex app-server 的 approval 事件翻译为 ACP permission request。

### 5.4 CLI 入口

在 `cmd/agent-bridge/main.go` 增加子命令：

```text
agent-bridge acp-agent claude-code [flags]
agent-bridge acp-agent codex [flags]
```

这些子命令只作为 ACP Agent server；它们不启动 chat plugins。

## 6. 配置设计

### 6.1 通用 ACP driver

```yaml
agent:
  driver: acp
  message_output:
    include:
      - answer
      - reasoning
      - action
      - artifact
      - diagnostic
  acp:
    command: "agent-bridge"
    args: ["acp-agent", "codex"]
    timeout: 30m
    env:
      CODEX_HOME: "${CODEX_HOME}"
```

### 6.2 Claude Code 后端

```yaml
agent:
  driver: acp
  acp:
    command: "agent-bridge"
    args:
      - "acp-agent"
      - "claude-code"
      - "--bin"
      - "claude"
      - "--permission-mode"
      - "default"
    timeout: 30m
```

### 6.3 Codex 后端

```yaml
agent:
  driver: acp
  acp:
    command: "agent-bridge"
    args:
      - "acp-agent"
      - "codex"
      - "--bin"
      - "codex"
      - "--listen"
      - "stdio://"
    timeout: 30m
```

### 6.4 Config 结构草案

```go
type Config struct {
    Agent struct {
        Driver string
        MessageOutput agent.MessageOutputOptions
        Opencode struct { ... }
        ACP struct {
            Command string
            Args []string
            Env map[string]string
            Timeout time.Duration
            InitializeTimeout time.Duration
            ShutdownTimeout time.Duration
        }
    }
}
```

## 7. 协议映射

### 7.1 Session

| `agent.Client` | ACP | Claude shim | Codex shim |
|---|---|---|---|
| `CreateSession` | `session/new` | 建 ACP session + Claude resume key | 建 ACP session + Codex thread |
| `GetSession` | `session/load` 或 shim state | 从 shim state 取 metadata | 从 Codex thread metadata 取 title/cwd |
| `ListSessions` | 可选 capability | shim state list | Codex thread list |

第一版如果后端不支持 list/load，可返回空列表或 “unsupported”，但 `/new` 和普通 prompt 必须可用。

### 7.2 Prompt

| `agent.Client` | ACP |
|---|---|
| `Prompt(sessionID, prompt, opts...)` | `session/prompt` |
| `PromptHandle.Done()` | ACP `session/prompt` response |
| `PromptHandle.Err()` | JSON-RPC error / process error |
| `PollMessagesAfter` | 读取本地聚合后的 update buffer |

第一版维持现有 bridge 的轮询模式：ACP driver 内部持续接收 `session/update`，`PollMessagesAfter` 从 buffer 中取出已完成或阶段性聚合的消息。

### 7.3 Message content

| ACP event/block | `agent.MessageContentKind` |
|---|---|
| assistant text chunk | `answer` |
| plan / thought summary | `reasoning` |
| tool call / command | `action.tool` |
| file diff / patch | `artifact.patch` |
| diagnostic / warning / error detail | `diagnostic` |

### 7.4 Permission / Question

| 当前接口 | ACP / shim 行为 |
|---|---|
| `ListPendingPermissions` | 读取 ACP permission request queue |
| `ReplyPermission(once)` | allow once |
| `ReplyPermission(always)` | allow always，若后端不支持则降级 once 并记录 warning |
| `ReplyPermission(reject)` | reject once |
| `ListPendingQuestions` | 读取 shim 生成的问题 queue |
| `ReplyQuestion` | 传回 shim；Claude/Codex 后端按各自协议继续 |

当前 `agent.PermissionReply` 没有 `reject_always`，第一版不暴露；需要时后续扩展枚举并更新 `/permission` 命令。

### 7.5 Cancel / timeout

- `handlePrompt` context cancel 或 prompt timeout 时，ACP driver 发送 `session/cancel`。
- 如果子进程未响应，在 `ShutdownTimeout` 后 kill。
- 如果 prompt 已经产出部分结果，保持现有行为：尽量保存最后一条可用输出。

## 8. 错误处理

1. initialize 失败：启动时返回 driver build error，插件不启动。
2. ACP version 不兼容：返回明确错误，提示支持版本与 agent 返回版本。
3. 子进程退出：当前 prompt 返回 bad gateway；driver 可按配置决定是否重启。
4. stdout 非 JSON：视为协议错误；stderr 采集到日志。
5. permission/question 已过期：映射为 `agent.ErrInteractionNoLongerPending`。
6. 后端能力缺失：`ListModels` / `ListAgents` 可返回空列表；核心 prompt 不受影响。

## 9. 安全边界

- 默认不启用 Claude `--dangerously-skip-permissions` 或 Codex bypass approvals。
- shim 配置必须显式传入危险模式；README 标注仅限隔离沙箱。
- ACP 子进程 environment 默认继承最小集合，敏感变量按 allowlist 传入。
- 工作目录来自 `PromptWithDirectory` / `/directory set`，shim 需要做 trim 和存在性校验。
- 日志不得记录完整 API key、auth token、bearer token。

## 10. 实施阶段

### 阶段 1：通用 ACP driver

产出：

- `internal/agent/acp` 实现 `agent.Client` MVP。
- fake ACP agent 单元测试。
- `agent.driver: acp` 配置接入。

验收：

- 可以连接 fake ACP agent。
- `/new` 创建 session。
- 普通 prompt 返回文本。
- cancel / timeout 可触发。

### 阶段 2：Codex shim

产出：

- `agent-bridge acp-agent codex`。
- Codex app-server event 到 ACP update 的映射。
- fake app-server 测试。

验收：

- 通过 OpenAI-compatible plugin 发消息，Codex 返回答案。
- Codex approval 请求能显示为 `/permission`。

### 阶段 3：Claude Code shim

产出：

- `agent-bridge acp-agent claude-code`。
- Claude stream-json parser。
- session resume 策略。

验收：

- 通过 OpenAI-compatible plugin 发消息，Claude 返回答案。
- stream-json 多段输出能聚合。
- 无账号/权限时给出明确错误。

### 阶段 4：文档与示例

产出：

- README 配置样例。
- `configs/config.acp-codex.yaml`。
- `configs/config.acp-claude.yaml`。
- 故障排查说明。

## 11. 验收标准

1. `go test ./...` 通过。
2. fake ACP agent contract tests 覆盖 initialize、session/new、prompt、update、permission、cancel。
3. 真实 Codex smoke 能完成一轮文本问答。
4. 真实 Claude smoke 能完成一轮文本问答。
5. 不改现有插件请求/响应格式。
6. 现有 opencode driver 测试不回归。
7. README 能让用户按配置启动至少一个 ACP 后端。

## 12. 主要风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| ACP 细节变动 | 类型/方法名可能调整 | ACP types 集中在 `internal/agent/acp/types.go`，引用官方 schema 更新 |
| Claude Code 非原生 ACP | permission/session 语义不完全匹配 | shim 内部维护 session state，第一版先支持安全非交互模式 |
| Codex app-server 仍是实验接口 | 事件模型可能变化 | Codex 映射隔离在 `acpshim/codex`，contract test 固定当前版本行为 |
| 流式与现有轮询模型差异 | 可能无法逐 token 转发 | 第一版聚合后输出；第二版再扩展 streaming bridge |
| 权限语义不一致 | allow/reject 维度丢失 | 先映射 once/always/reject，metadata 保留原始详情 |

## 13. 后续扩展

- `agent-bridge` 自身作为 ACP Agent server，让 IDE 直接连接。
- ACP file operation / terminal operation 完整支持。
- rich diff / patch card 输出到 UME。
- `reject_always` 和更细粒度 permission policy。
- 多模态 prompt。
- streaming reply 直推聊天插件。
