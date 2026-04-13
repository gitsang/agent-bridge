# 关于数据结构

## Model

一个 Model 通常包含 Provider 和 Model 两个部分。并有 ID 和 Name 两种表示方式。

我们可以将其分为三种格式 `ModelSpec` `ModelRef` 和 `ModelInfo`

### ModelSpec

`ModelSpec` 为字符串形式的模型名称，格式为 `[{providerID}/]{modelID}`（如：`azure/claude-sonnet-4-6` `gpt-5.3-codex` `openai/gpt-5.4`）。

我们要求（约定） providerID 必须不包含 `/`，因为我们以第一个 `/` 作为分隔符区分 ProviderID 和 ModelID。

> [!NOTE]
>
> 这主要是考虑到有很多人会使用多重中转的模型，所以可能会出现多个斜杠的情况，比如 `bifrost/openrouter/z-ai/glm-5.1` 表示自建的 bifrost 网关，对接的 openrouter 供应商，的 z-ai 组织的 glm-5.1 模型。
>
> 如果真的要区分，那么供应商可能会变成一个数组。
> 但我们认为，实际上用户通常不会那么的关注中间每一层的供应商，也不会有单独更换他们的需求，
> 因为最顶层的供应商才决定了我用哪个 API Key。并且对于我来说，这就是 `bifrost` 提供的 `openrouter/z-ai/glm-5.1` 模型。
>
> 并且我们的 providerID 是可以省略的，因为更多时候，我们只会使用一个供应商提供一个模型，特别是当我们使用了自建网关时

### ModelRef

`ModelRef` 几乎所有实例间传递和存储的数据类型，里面只包含 ProviderID 和 ModelID，不包含 Name：

```go
type ModelRef struct {
    ProviderID string
    ModelID    string
}
```

这是最干净的一个数据结构，不允许省略，他必须唯一的确定一个模型。

无论从什么地方获取到数据，但你要将 Model 信息传递给其他组件或实例时，甚至是自己处理时，都必须先转换为 `ModelRef`，所有的其他格式都不应该持久的存在。

### ModelInfo

`ModelInfo` 只为展示使用，他在 `ModelRef` 上额外增加一个 Name 属性，
只有在需要展示时才会获取（富化）他的 Name 属性，并且只有在需要展示时，才应该使用这个数据结构。

```go
type ModelInfo struct {
    ModelRef
    ProviderName string
    ModelName    string
}
```

## Message

消息数据应该只包含 **消息事实**，而不是 **控制参数**。

比如：

```go
type Message struct {
	ID        string
	SessionID string
	Role      string
	Content   string

	Agent string
	Model ModelRef
}
```

这里的 `ID` `Content` `Role` 都是属于消息事实，表示的就是这个消息的 ID 是什么，由谁发出，内容是什么。

而 `Model` `Agent` 在请求和响应的场景，则可能具有不同的含义：
在响应中表示的是消息事实，表示这个消息使用的是哪个模型，哪个 Agent 生成。
而在请求中，由于大部分消息由用户发送，`Model` `Agent` 要么不具备含义（用户的输入不可能从概念上由模型生成），
要么应该表示的是用户希望下一次的响应使用哪个模型，哪个 Agent 生成。
这就是所谓的 **控制参数**

对于需要控制参数的场景，我们不应该直接使用 Message 数据结构，我们应该保持 Message 数据的纯净。

比如 `Prompt()` 函数，可能需要发送时设置 Agent/Model 等信息，就应该使用 Functional Options 等进行设置，而不能直接发送 Message 进行设置。

```go
// Bad
Prompt(ctx context.Context, request *Message) (*PromptHandle, error)

// Good
Prompt(ctx context.Context, sessionID string, prompt string, optfs ...PromptOptionFunc) (*PromptHandle, error)
```
