# ACP 对接试跑与验证方案

日期：2026-05-19  
状态：设计草案，等待实现  
目标：在不依赖真实模型账号的前提下先验证 ACP driver contract，再用真实 Codex / Claude Code 做最小 smoke。

## 1. 试跑分层

试跑分四层，从低风险到真实后端逐级推进：

1. **静态验证**：格式化、lint、unit test。
2. **fake ACP contract**：用本地 fake ACP Agent 验证协议行为。
3. **shim contract**：用 fake Claude/Codex 输出验证 shim parser 和映射。
4. **真实 smoke**：连接本机 `codex` / `claude` CLI 做一轮实际问答。

任何一层失败，都先修该层，不跳到下一层。

## 2. 阶段 0：前置检查

命令：

```bash
git status --short
go test ./...
command -v codex || true
codex --version || true
command -v claude || true
claude --version || true
```

预期：

- 当前已有测试通过，确保不是 ACP 改动引入旧功能回归。
- 若 `codex` 或 `claude` 不存在，只跳过真实 smoke，不影响 fake contract。

## 3. 阶段 1：ACP driver fake contract

### 3.1 Fake ACP Agent 行为

实现一个测试内 fake process 或 fake transport，模拟：

1. 收到 `initialize`，返回支持的 protocol version 和 capabilities。
2. 收到 `session/new`，返回 `sessionId: fake-session-1`。
3. 收到 `session/prompt`：
   - 发送 `session/update` assistant chunk：`hello`
   - 发送 `session/update` assistant chunk：` world`
   - 返回 stop reason：`end_turn`
4. 收到 permission 场景 prompt 时：
   - 发送 permission request。
   - 等待 driver reply。
   - 继续发送 assistant result。
5. 收到 cancel 场景 prompt 时：
   - 等待 `session/cancel`。
   - 返回 cancelled。

### 3.2 验证用例

```bash
go test ./internal/agent/acp -run 'TestClient'
```

用例清单：

- `TestClientInitializeNegotiatesVersion`
- `TestClientCreateSessionSendsSessionNew`
- `TestClientPromptAggregatesAssistantChunks`
- `TestClientPollMessagesAfterReturnsBufferedUpdates`
- `TestClientPermissionRequestAndReply`
- `TestClientCancelOnContextDone`
- `TestClientRejectsMalformedJSONRPC`
- `TestClientMapsProcessExitToError`

通过标准：

- 所有测试稳定通过。
- race test 不报数据竞争。

## 4. 阶段 2：bridge 集成 fake ACP

使用 `bridge.AgentBridge` + `acp.Client` + fake ACP Agent，验证现有 slash command 不回归。

命令：

```bash
go test ./internal/bridge -run 'TestHandle.*ACP|Test.*Permission|Test.*Question'
```

用例：

- 普通聊天自动创建 ACP session。
- `/new --directory /tmp/project --title demo` 传到 `session/new`。
- `/session attach` 可绑定 ACP session id。
- `/permission once` 调用 ACP reply。
- `/question` 调用 ACP/shim question reply。

通过标准：

- bridge 不需要理解 ACP 细节。
- 现有 opencode 测试仍通过。

## 5. 阶段 3：Codex shim contract

### 5.1 Fake Codex app-server

用 fake JSON-RPC transport 模拟 Codex app-server：

- thread 创建成功。
- turn submit 成功。
- 发送 assistant text item。
- 发送 approval request。
- 支持 cancel。

### 5.2 验证命令

```bash
go test ./internal/agent/acpshim/codex -run 'Test'
```

通过标准：

- Codex item/event 能转成 ACP `session/update`。
- approval 能转成 ACP permission。
- app-server 异常能返回可读错误。

## 6. 阶段 4：Claude Code shim contract

### 6.1 Fake Claude CLI

用 shell script 或 Go fake command 输出固定 JSONL：

```jsonl
{"type":"system","subtype":"init","session_id":"claude-session-1"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}
{"type":"result","subtype":"success","session_id":"claude-session-1"}
```

### 6.2 验证命令

```bash
go test ./internal/agent/acpshim/claude -run 'Test'
```

通过标准：

- stream-json parser 可解析多事件。
- assistant text 能聚合。
- session id 能保存到 shim state。
- CLI exit code 非 0 时返回 stderr 摘要。

## 7. 阶段 5：端到端 fake smoke

使用 `agent-bridge` 二进制、OpenAI-compatible plugin、fake ACP Agent。

配置示例：

```yaml
plugins:
  openai-compatible:
    openai-compatible:
      listen: ":24368"

agent:
  driver: acp
  acp:
    command: "./testdata/fake-acp-agent"
    args: []
    timeout: 30s
```

启动：

```bash
go run ./cmd/agent-bridge -c /tmp/agent-bridge-acp-fake.yaml
```

请求：

```bash
curl -sS http://127.0.0.1:24368/chat/completions \
  -H 'content-type: application/json' \
  -d '{
    "model":"test",
    "user":"trial-chat-1",
    "messages":[{"role":"user","content":"say hello"}]
  }' | jq .
```

通过标准：

- HTTP 200。
- `choices[0].message.content` 包含 fake agent 返回文本。
- agent session 绑定可通过 `/session current` 查到。

## 8. 阶段 6：真实 Codex smoke

前置：

```bash
command -v codex
codex doctor || true
```

配置：

```yaml
agent:
  driver: acp
  acp:
    command: "agent-bridge"
    args: ["acp-agent", "codex", "--bin", "codex", "--listen", "stdio://"]
    timeout: 10m
```

请求：

```bash
curl -sS http://127.0.0.1:24368/chat/completions \
  -H 'content-type: application/json' \
  -d '{
    "model":"codex",
    "user":"codex-smoke-1",
    "messages":[{"role":"user","content":"用一句话说明当前目录是什么项目。不要改文件。"}]
  }' | jq -r '.choices[0].message.content'
```

通过标准：

- 有文本答案。
- 未产生意外工作区 diff：`git status --short` 不出现 smoke 导致的代码改动。
- 如需权限，聊天里出现 `/permission` 指引，回复后可继续。

## 9. 阶段 7：真实 Claude Code smoke

前置：

```bash
command -v claude
claude doctor || true
```

配置：

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
    timeout: 10m
```

请求：

```bash
curl -sS http://127.0.0.1:24368/chat/completions \
  -H 'content-type: application/json' \
  -d '{
    "model":"claude",
    "user":"claude-smoke-1",
    "messages":[{"role":"user","content":"用一句话说明当前目录是什么项目。不要改文件。"}]
  }' | jq -r '.choices[0].message.content'
```

通过标准：

- 有文本答案。
- Claude stream-json 事件被正确聚合。
- 无意外工作区 diff。

## 10. 回归矩阵

每个阶段完成后运行：

```bash
go test ./...
```

发布前运行：

```bash
make fmt
make test
make vet
```

如果 `golangci-lint` 已安装，再运行：

```bash
make lint
```

## 11. 问题定位规则

- fake ACP 失败：先看 `internal/agent/acp/jsonrpc.go` 和 transport 日志。
- bridge 集成失败：先确认 `agent.Client` 行为是否与现有 opencode fake 一致。
- Codex smoke 失败：先用 `codex app-server --listen stdio://` 或生成 schema 确认当前 CLI 协议是否变化。
- Claude smoke 失败：先直接运行 `claude -p --output-format stream-json "hello"`，确认认证、权限和输出格式。
- 权限卡住：确认 pending queue 是否有 request，聊天侧 `/permission` 是否带正确 session id。

## 12. 停止条件

满足以下条件即可认为试跑通过：

1. fake ACP contract 全部通过。
2. bridge 集成测试通过。
3. Codex 或 Claude 至少一个真实 smoke 通过。
4. `go test ./...` 通过。
5. 工作区无 smoke 产生的非预期代码改动。
