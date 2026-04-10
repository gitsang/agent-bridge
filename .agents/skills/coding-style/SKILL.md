---
name: coding-style
description: 总结并应用 Go 代码规范与实现风格。只要用户提到修改这个仓库、按仓库风格实现功能、补充或重构 Go 代码、总结项目规范，或者希望代码“符合项目现有写法”，都应使用这个 skill。
---

# Agent Bridge 代码风格

通用的 Go 实践，风格：包小而清晰、控制流直接、接口收敛、抽象克制、测试简单明确。

## 总体原则

- 先匹配现有模式，再考虑新增抽象。
- 优先写小而直接的代码，不为了“复用感”制造额外层级。
- API 要直观；事情简单，实现也应简单。
- 保持现有目录职责：`cmd/` 放启动与装配，`internal/` 放业务实现，`pkg/` 只放真正可复用的小工具。
- 完成前用真实 Go 命令验证行为。

## 代码形态

### 构造函数

- 使用 `New` 或 `NewType` 命名。
- 仓库现有模式是返回具体指针类型，就继续保持。
- 构造函数尽量短小、直白。
- 简单依赖注入优先使用直接作用于目标对象的 option function：

```go
type OptionFunc func(*AgentBridge)

func WithAgentClient(client sessionClient) OptionFunc {
	return func(target *AgentBridge) {
		target.agentClient = client
	}
}

func New(optfs ...OptionFunc) *AgentBridge {
	connector := &AgentBridge{}
	for _, apply := range optfs {
		if apply == nil {
			continue
		}
		apply(connector)
	}
	return connector
}
```

- 如果只是给单个对象注入几个依赖，不要额外引入中间 `Options` 结构体。
- option helper 延续 `WithXxx` 命名。
- 只有在“需要聚合默认值/配置项”时，才考虑 `Options` 结构体；默认不要上更重的模式。

### 接口

- 接口保持小而聚焦，只表达调用方真正需要的能力。
- 如果接口只服务于单个 package 内的依赖反转，优先把它定义在消费方附近。
- 命名使用能力或角色名，例如 `Plugin`、`Store`、`sessionClient`。

### 结构体与配置

- 结构体保持显式，除非仓库已有先例，否则不要为了省几行代码去嵌入匿名字段。
- 配置结构体需要时同时保留 `json`、`yaml`、`default`、`usage` 等 tag。
- 自然分组的配置用嵌套结构体表达，不要摊平成很长的一层。

## 控制流与错误处理

- 参数校验尽量前置，优先 guard clause。
- 多用早返回，少堆嵌套分支。
- 普通错误直接用 `fmt.Errorf(...)`。
- 需要保留底层错误语义时使用 `%w`。
- 只有当上层确实要按类型分支处理时，才定义 typed error，例如 HTTP 状态码映射。
- 错误信息保持直接、简短、英文小写风格，跟现有仓库一致。
- 优先使用显式错误判断：`if err != nil { return nil, err }`，不要套一层无意义 helper。

## 命名与组织

- package 名保持小写且简短。
- 文件名保持简单；测试文件使用 `_test.go`。
- 导出名要明确，私有名只有在作用域很小的时候才缩短。
- `WithXxx`、`ParseXxx`、`BuildXxx`、`GetXxx`、`ListXxx` 这些命名只在贴合现有语义时使用。
- 新功能优先放到最接近职责的位置，不要轻易拆出新层。

## 并发与生命周期

- 请求链路和长生命周期操作都显式接收 `context.Context`。
- 应用装配层的并发任务优先使用 `errgroup.WithContext` 管理生命周期。
- 小型内存注册表或存储在确有并发访问时使用 `sync.RWMutex`。
- 生命周期控制保持简单，不要为了“通用”引入复杂 worker 抽象。

## 日志

- 使用 `log/slog` 做结构化日志。
- 通过 `logger.With(...)` 挂稳定字段。
- 日志要有信息量，但不要在核心逻辑里堆过多噪音。

## 测试风格

- 只使用标准库 `testing`。
- 除非有明确理由，否则测试开头调用 `t.Parallel()`。
- 当场景数量不多时，优先“一条场景一个测试函数”，不要为了形式统一硬改成 table-driven。
- 测试命名使用 `Test<Function><Scenario>`。
- fake 直接写在同一个 `_test.go` 文件里。
- fake 同时承担“记录输入”和“提供返回结果/错误”的职责。
- 断言直接使用 `t.Fatal` / `t.Fatalf`。
- 值比较优先使用 `got/want` 表达。

示例断言：

```go
if err != nil {
	t.Fatalf("Handle() error = %v", err)
}
if got, want := resp.SessionID, "existing-session"; got != want {
	t.Fatalf("Handle() session = %q, want %q", got, want)
}
```

## 插件与装配模式

- `cmd/agent-bridge` 只负责启动、配置、日志、依赖装配和进程生命周期。
- 核心请求处理放在 `internal/bridge`。
- 插件侧的协议/传输适配放在 `internal/plugin/<type>`。
- `init()` 只用于插件注册，不要把通用应用装配写进去。
- 插件构造显式接收基础设施依赖，不要偷偷读全局状态。

## 这个仓库里尽量避免的事

- 不要为了省事引入额外 assertion / mocking 库。
- 不要给简单重复逻辑套泛化 helper 层。
- 不要在 package 边界上提前抽出很宽的接口，除非真的存在多种实现。
- 不要把小而清晰的测试强行改成 table-driven。
- 不要为简单问题使用过重的 options 模式。

## 工作检查清单

在这个仓库里实现功能时：

1. 先读附近文件，匹配已有命名、构造方式和错误处理风格。
2. 新接口保持窄、小、局部。
3. 轻量 DI 优先使用 `OptionFunc` 直接修改目标对象。
4. 用本地 fake 补齐或更新聚焦测试。
5. 结束前运行 `go test ./...` 和 `go build ./...`。
6. 如果只改了单个 package，可以先跑该 package，再跑全仓库。

## 仓库内参考文件

- `internal/bridge/connect.go`：轻量 functional options 与请求校验
- `internal/agent/client.go`：`WithXxx` 命名，以及在复杂度足够时使用 `Options` 聚合默认值
- `internal/bridge/parser.go`：显式解析与 guard clause 风格
- `internal/bridge/connect_test.go` 与 `internal/bridge/parser_test.go`：测试命名、fake 写法、断言风格
- `internal/plugin/openai_compatible/plugin.go`：插件构造、HTTP 适配、typed error 映射
- `cmd/agent-bridge/main.go`：应用装配与 `errgroup` 生命周期管理

如果某个改动和这些模式冲突，默认选择更简单、更贴近现有代码的实现方式，除非上下文已经明确需要更强的抽象。
