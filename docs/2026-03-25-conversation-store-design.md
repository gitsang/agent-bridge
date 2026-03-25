# Conversation Store + Slash Command Design

## Goal

Unify message handling around a single `connect`-owned model so that:

- plugins only handle chat transport concerns
- `connect` owns all slash-command parsing and execution
- `connect` owns chat-session to opencode-session binding
- opencode-related state is grouped under `Message.Opencode`

This design replaces the current split where plugins partially own session binding and `connect` only handles a subset of message parsing.

## Agreed Data Model

```go
type Message struct {
	Content  string          `json:"content"`
	Chat     ChatContext     `json:"chat,omitempty"`
	Opencode OpencodeContext `json:"opencode,omitempty"`
}

type ChatContext struct {
	SessionID string `json:"session_id,omitempty"`
}

type OpencodeContext struct {
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Model     string `json:"model,omitempty"`
	Workdir   string `json:"workdir,omitempty"`
}
```

### Semantics

- `Message.Content`: message body from user input or assistant output
- `Message.Chat.SessionID`: chat-platform conversation identity supplied by the plugin
- `Message.Opencode.SessionID`: opencode session identity resolved by `connect`
- `Message.Opencode.Title`: opencode session title
- `Message.Opencode.Model`: model override or resolved model metadata
- `Message.Opencode.Workdir`: workdir override or resolved workdir metadata

This removes the current ambiguity where one top-level `SessionID` field tries to represent different identities at different layers.

## Ownership Boundaries

### Plugin Responsibilities

Plugins should only own transport-specific behavior:

- decode inbound requests
- extract chat context such as chat session ID
- perform transport-specific deduplication and validation
- pass normalized `Message` values into `connect`
- render and deliver responses using returned `Message`

Plugins should no longer own chat-to-opencode binding.

### Connect Responsibilities

`connect` should own all opencode-related orchestration:

- slash-command parsing
- slash-command dispatch and help generation
- chat-session to opencode-session binding
- conversation-level default model and workdir state
- opencode session creation and lookup
- prompt execution
- response context hydration

### Opencode Client Responsibilities

`internal/opencode` should remain a thin SDK wrapper:

- session operations
- prompt operations
- model/provider listing
- no awareness of chat platforms or chat binding

## ConversationStore

Use `ConversationStore` as the central state owner for each chat conversation.

### Store Model

```go
type ConversationState struct {
	ChatSessionID     string
	OpencodeSessionID string
	DefaultModel      string
	DefaultWorkdir    string
	BoundAt           time.Time
	LastSeenAt        time.Time
}
```

### Interface

```go
type ConversationStore interface {
	Get(chatSessionID string) (ConversationState, bool)
	PutBinding(chatSessionID, opencodeSessionID string)
	SetDefaultModel(chatSessionID, model string)
	SetDefaultWorkdir(chatSessionID, workdir string)
	Delete(chatSessionID string)
	Touch(chatSessionID string)
	List() []ConversationState
}
```

### Default Implementation

Provide an in-memory implementation in `internal/connect/conversation_store.go`:

- `sync.RWMutex` protected map
- TTL-based cleanup
- optional max-size eviction
- overwrite existing binding on re-attach
- process-local and non-persistent, matching current behavior

### Why ConversationStore Instead of BindingStore

The store must eventually hold more than a simple binding:

- bound opencode session
- default model for the conversation
- default workdir for the conversation

`ConversationStore` better matches its actual responsibility and future growth.

## Command Model

Only slash-commands are supported. Head directives such as `@session:` and `@model:` should be removed.

### Supported Commands

- `/new [--model <model>] [--work-dir <dir>] [--title <title>]`
- `/session attach <opencode-session-id>`
- `/session detach`
- `/session current`
- `/session list [--work-dir <dir>]`
- `/model set <model>`
- `/model list`
- `/workdir set <dir>`
- `/help [command]`

### Command Semantics

#### `/new`

- creates a fresh opencode session
- optionally applies model and workdir overrides
- stores the created session into `ConversationStore` when `Chat.SessionID` is present
- returns the resolved `OpencodeContext`

#### `/session attach`

- binds the current chat conversation to an existing opencode session
- validates that the target opencode session exists
- updates `ConversationStore`

#### `/session detach`

- removes the bound opencode session and conversation defaults
- next plain message creates a new opencode session unless another attach occurs

#### `/session current`

- returns the current conversation binding and defaults
- if no binding exists, returns a friendly informational message

#### `/session list`

- lists opencode sessions
- may filter by `--work-dir`
- does not mutate conversation state

#### `/model set`

- sets the default model for the current chat conversation in `ConversationStore`
- affects subsequent plain messages in that conversation

#### `/model list`

- lists available models from opencode providers
- may optionally respect the effective workdir context

#### `/workdir set`

- sets the default workdir for the current chat conversation in `ConversationStore`
- affects subsequent plain messages and session listing defaults

#### `/help`

- returns concise usage text for all commands or one specific command

## Command Parser Design

Implement a custom parser under `internal/connect/command/` rather than using Cobra.

### Why Not Cobra

- Cobra is process-CLI oriented while this parser runs inside a long-lived request handler
- Cobra requires pre-tokenized args and does not solve raw chat message tokenization
- Cobra command trees are stateful and awkward for concurrent request handling
- Cobra help and error output are terminal-oriented instead of chat-oriented

### Parser Requirements

- detect commands by leading `/`
- support subcommands
- support positional args
- support `--flag value`
- support `--flag=value`
- support single and double quoted values
- return chat-friendly parse errors

### Core Types

```go
type Invocation struct {
	Path  []string
	Args  []string
	Flags map[string]string
}

type Command interface {
	Path() []string
	Summary() string
	Execute(ctx context.Context, req *Message, inv Invocation) (*Message, error)
}
```

### Registry

```go
type Registry struct {
	commands map[string]Command
}
```

The registry should:

- register command handlers by normalized path
- resolve invocation targets
- produce help text from registered metadata

## Connect Request Flow

### Plain Message Flow

For a non-command message, `connect.Handle` should execute this resolution order:

1. load conversation state using `Chat.SessionID`
2. resolve effective opencode session:
   - explicit `req.Opencode.SessionID`, if ever supplied internally
   - else conversation binding from `ConversationStore`
   - else create a new opencode session
3. resolve effective model:
   - request-level `req.Opencode.Model`
   - else conversation default model
4. resolve effective workdir:
   - request-level `req.Opencode.Workdir`
   - else conversation default workdir
5. call opencode prompt
6. return hydrated `Message` with full `OpencodeContext`
7. update `ConversationStore` binding if `Chat.SessionID` is present

### Slash Command Flow

For a command message:

1. parse `Content` into `Invocation`
2. dispatch through the command registry
3. execute command against `ConversationStore` and opencode client as needed
4. return a normal `Message` response with:
   - `Content` describing the result
   - `Chat` copied through
   - `Opencode` filled where relevant

## Response Rendering Contract

Plugins should render directly from the returned `Message`.

Recommended rendering inputs:

- `Content`
- `Opencode.Title`
- `Opencode.SessionID`
- `Opencode.Model`
- `Opencode.Workdir`

This makes plugin rendering simpler because all opencode context is grouped together.

## Plugin Migration

### UME Plugin

Keep in plugin:

- webhook decode
- `msgId` dedupe
- access token handling
- response delivery

Remove from plugin:

- `chatSessionID -> opencodeSessionID` binding ownership
- session binding lookup before calling `connect`
- session binding write-back after response

New request shape into `connect`:

```go
connect.Message{
	Content: message,
	Chat: connect.ChatContext{
		SessionID: chatSessionID,
	},
}
```

### OpenAI-Compatible Plugin

Map `request.User` into `Chat.SessionID`.

Remove plugin-local anonymous session binding state:

- remove `lastSessionID`
- stop reusing session IDs inside the plugin
- let `connect` own this state through `ConversationStore`

New request shape into `connect`:

```go
connect.Message{
	Content: message,
	Chat: connect.ChatContext{
		SessionID: strings.TrimSpace(req.User),
	},
}
```

If `req.User` is empty, the conversation is anonymous and `connect` may choose not to persist state for it.

## Opencode Client Changes

Refactor the opencode wrapper to use request/response structs instead of scattered scalar parameters.

### Prompt API

```go
type PromptRequest struct {
	SessionID string
	Content   string
	Model     string
	Workdir   string
}

type PromptResult struct {
	Reply    string
	Session  OpencodeContext
}
```

The prompt implementation should forward:

- `SessionID`
- `Content`
- `Model` when present
- `Workdir` when present

### Additional Client APIs

- `ListSessions(ctx, workdir string)`
- `ListModels(ctx, workdir string)`
- existing `GetSession` and `CreateSession`, extended as needed for workdir/title

## Error Model

Errors returned to plugins should stay chat-friendly.

Examples:

- `unknown command: /sessoin`
- `missing opencode session id`
- `conversation is not attached to any opencode session`
- `session not found: ses_xxx`

Suggested status mapping:

- invalid command syntax: `400`
- missing required args: `400`
- unknown command: `400`
- opencode lookup or prompt failure: `502`
- internal wiring failure: `500`

## Migration Plan

### Phase 1: Message and Store Foundation

- replace the old `Message` layout with the new nested structure
- introduce `ConversationStore`
- update `connect` to consume `Chat.SessionID`
- update plugins to pass `Chat.SessionID`
- keep current minimal command set temporarily if needed

### Phase 2: Slash Command Runtime

- add custom tokenizer and invocation parser
- add registry-based command execution
- implement `/new`
- implement `/session attach`, `/session detach`, `/session current`
- remove old head directives

### Phase 3: Conversation Defaults

- implement `/model set` and `/model list`
- implement `/workdir set`
- make plain messages inherit conversation defaults

### Phase 4: Cleanup

- remove legacy `Message.Command`
- remove plugin-owned binding logic and tests that depend on it
- simplify plugin rendering around `Message.Opencode`

## Testing Strategy

### Connect Tests

- plain message creates session when no binding exists
- plain message reuses bound opencode session
- `/session attach` stores binding
- `/session detach` removes binding
- `/session current` reports current state
- `/model set` updates defaults
- `/workdir set` updates defaults
- command parser handles quoted paths correctly

### Plugin Tests

- UME still strips mentions and deduplicates by `msgId`
- UME passes `Chat.SessionID` into `connect`
- OpenAI-compatible passes `User` into `Chat.SessionID`
- plugin no longer stores opencode session bindings locally

### End-to-End Behavior

- attach a chat conversation to an existing opencode session
- send plain follow-up messages and verify session reuse
- set default model/workdir and verify they are applied on later prompts

## Open Questions

These do not block the design, but should be decided during implementation:

- should anonymous OpenAI requests without `user` be stateful or always create a fresh conversation?
- should `/session detach` preserve `DefaultModel` and `DefaultWorkdir`, or clear everything?
- should `/new` automatically replace the bound opencode session for the current conversation? Recommended: yes.

## Recommendation

Adopt this design as the new architecture baseline:

- nested `Message` context model
- `ConversationStore` in `connect`
- slash-command-only parser and registry
- plugin transport-only responsibilities

This gives a consistent ownership model, eliminates split command behavior, and makes future command growth straightforward.
