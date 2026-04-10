# Conversation Store Implementation Plan

## Objective

Implement the `ConversationStore + slash-command` architecture described in `docs/2026-03-25-conversation-store-design.md` with minimal disruption and clear phase boundaries.

## Scope

In scope:

- new `Message` contract with nested `Chat` and `Opencode` contexts
- `bridge`-owned `ConversationStore`
- slash-command parser and registry in `bridge`
- migration of plugin-owned session binding into `bridge`
- opencode client request/response shaping for model/workdir/session context
- tests and docs updates for the new architecture

Out of scope:

- persistent store backend (in-memory only for now)
- plugin transport protocol redesign
- advanced shell features in command parser beyond quoted args and basic flags

## Deliverables

- updated `internal/bridge/message.go` data model
- `internal/bridge/conversation_store.go` (and tests)
- `internal/bridge/command/*` parser and command handlers
- updated `internal/bridge/connect.go` orchestration flow
- updated plugin integrations for UME and OpenAI-compatible
- updated `internal/agent` APIs for prompt/session/model operations
- green test suite for modified packages

## Phase Plan

## Phase 1: Data Contract Foundation

### Goals

- introduce the new message schema and preserve compile health
- establish context semantics before behavior changes

### Tasks

1. Update `internal/bridge/message.go`
   - replace old fields with:
     - `Content string`
     - `Chat ChatContext`
     - `Opencode AgentContext`
   - define `ChatContext` and `AgentContext` types
   - remove legacy `Command` field

2. Update direct call sites to compile
   - `internal/bridge/connect.go`
   - `internal/plugin/openai_compatible/plugin.go`
   - `internal/plugin/ume/plugin.go`
   - relevant tests in `internal/bridge` and plugin packages

3. Preserve behavior parity where possible
   - map old `Message` usage to new nested fields without functional changes yet

### Exit Criteria

- code compiles with new message schema
- no remaining references to removed top-level fields

## Phase 2: ConversationStore Introduction

### Goals

- centralize chat-session state in `bridge`
- stop plugin ownership of chat↔opencode binding

### Tasks

1. Add `internal/bridge/conversation_store.go`
   - define `ConversationState`
   - define `ConversationStore` interface
   - implement `MemoryConversationStore` with mutex + TTL + optional max-size policy

2. Wire store into `OpencodeConnect`
   - add store field to `OpencodeConnect`
   - add constructor option such as `WithConversationStore(...)`
   - default to memory store when not provided

3. Move binding logic from plugin to `bridge`
   - UME plugin: remove opencode binding lookup/writeback
   - OpenAI-compatible plugin: remove `lastSessionID`
   - both plugins pass `Chat.SessionID` only

4. Keep transport-specific responsibilities in plugin
   - UME dedupe by `msgId` remains in plugin
   - payload decode/encode remains in plugin

### Exit Criteria

- `bridge` resolves/reuses opencode sessions using `ConversationStore`
- plugins no longer maintain chat↔opencode mapping state

## Phase 3: Slash-Command Runtime

### Goals

- replace ad-hoc parser with a command runtime under `bridge`
- support only slash-command syntax

### Tasks

1. Create command parser module
   - `internal/bridge/command/parser.go`
   - implement quote-aware tokenizer
   - parse command path, positional args, and flags (`--x y`, `--x=y`)

2. Create command registry
   - `internal/bridge/command/registry.go`
   - register handlers by normalized command path
   - generate command help metadata

3. Implement command handlers
   - `session attach`
   - `session detach`
   - `session current`
   - `session list`
   - `new`
   - `model set`
   - `model list`
   - `workdir set`
   - `help`

4. Integrate command dispatch in `bridge.Handle`
   - detect slash commands from `Message.Content`
   - dispatch via registry
   - return standard `Message` output with `AgentContext`

5. Remove legacy directive parser behavior
   - remove `@session:` / `@model:` handling
   - ensure clear errors for unsupported legacy syntax

### Exit Criteria

- slash-command-only behavior is active
- command parser supports quoted args and subcommands
- command outputs are rendered via regular `Message`

## Phase 4: Opencode Client Alignment

### Goals

- align internal client APIs with new context-driven flow

### Tasks

1. Refactor prompt request/response contracts
   - add prompt request struct with session/model/workdir/content
   - return structured response with opencode context fields

2. Add listing APIs needed by commands
   - list sessions with optional workdir filter
   - list models/providers (and optional workdir scope if supported)

3. Update `bridge` command handlers and plain message flow
   - use new client APIs consistently

### Exit Criteria

- all command handlers use typed opencode client methods
- model/workdir/session context flows end-to-end

## Phase 5: Rendering and UX Consistency

### Goals

- ensure plugin rendering works with unified context model

### Tasks

1. Update UME response formatter
   - use `Message.Content`
   - use `Message.Agent.{Title,SessionID,Model,Workdir}`

2. Update OpenAI-compatible response conversion
   - return assistant text from `Message.Content`
   - ensure user/session mapping uses `Chat.SessionID`

3. Normalize command responses
   - concise content text
   - include relevant opencode context when applicable

### Exit Criteria

- both plugins render from the same message contract
- command and plain responses are consistent

## Phase 6: Test and Validation

### Goals

- guarantee correctness of parser, binding state, and flow integration

### Tasks

1. Unit tests for parser
   - quoted args
   - flag formats
   - unknown commands
   - malformed syntax

2. Unit tests for `ConversationStore`
   - put/get/delete
   - TTL cleanup
   - overwrite semantics
   - concurrency safety

3. Connect integration tests
   - plain message session creation and reuse
   - slash command behaviors (`attach`, `detach`, `current`, `new`)
   - model/workdir defaults applied to plain messages

4. Plugin tests
   - UME still deduplicates
   - UME passes `Chat.SessionID` correctly
   - OpenAI-compatible no longer uses local session memory

5. Full verification runs
   - `go test ./...`
   - targeted package tests for faster iteration loops

### Exit Criteria

- modified package tests pass
- no regressions in plugin transport behavior

## Sequencing and Dependency Map

Recommended implementation order:

1. Phase 1 (schema)
2. Phase 2 (store + binding ownership move)
3. Phase 3 (commands)
4. Phase 4 (opencode API alignment)
5. Phase 5 (rendering polish)
6. Phase 6 (final hardening)

Critical dependencies:

- command handlers depend on `ConversationStore`
- plugin migration depends on new message schema
- connect flow stabilization depends on opencode client API alignment

## Risk Register

1. **Semantic drift during schema migration**
   - mitigation: do Phase 1 as compile-safe rename/reshape before behavior changes

2. **Behavior regressions in plugin session continuity**
   - mitigation: explicit before/after tests for multi-turn conversations

3. **Parser edge cases for quoted paths**
   - mitigation: parser-focused tests before command rollout

4. **Concurrency issues in store**
   - mitigation: mutex-protected store + race-focused test cases

5. **Unclear fallback when `Chat.SessionID` missing**
   - mitigation: define explicit policy (ephemeral conversation; no persisted store state)

## Implementation Checklist

- [ ] message schema switched to `Content/Chat/Opencode`
- [ ] conversation store implemented and injected
- [ ] plugins no longer own opencode binding
- [ ] slash-command parser and registry live
- [ ] command handlers implemented
- [ ] legacy head directives removed
- [ ] opencode client APIs aligned
- [ ] plugin rendering switched to `Message.Agent`
- [ ] tests updated and passing

## Suggested PR Strategy

If split into multiple PRs:

1. PR-A: message schema + compile migration
2. PR-B: conversation store + connect ownership
3. PR-C: slash-command parser + core command handlers
4. PR-D: opencode client alignment + model/workdir commands
5. PR-E: cleanup + docs + test hardening

If done in one PR, keep commit history logically grouped by these phases.
