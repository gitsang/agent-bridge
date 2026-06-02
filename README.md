<!-- MarkdownTitleNumber auto -->

# Agent Bridge

<p align="center">
  <a href="./README.md">English</a> | <a href="./README.zh-CN.md">中文</a>
</p>

A bridge connecting AI agents with chat applications, enabling interaction with AI programming assistants (such as Claude Code, Codex, OpenCode) through chat software like Mattermost, OpenAI-compatible interfaces, etc.

## 1. Core Philosophy

### 1.1 Design Principles

The core design philosophy of Agent Bridge is **separation of concerns** and **unified abstraction**:

1. **Platform-Agent Separation**: Chat platforms only handle message sending/receiving, AI agents only handle code generation and interaction, with Agent Bridge connecting them
2. **Unified Interface Design**: All chat platforms implement the same `Platform` interface, all AI agents implement the same `Agent` interface, enabling plug-and-play functionality
3. **Streaming Response Support**: Supports streaming output, allowing users to see AI agent's thinking process and generation results in real-time
4. **Session State Management**: Maintains binding relationships between chat sessions and AI agent sessions, supporting multi-turn conversations and session switching

### 1.2 Architecture Overview

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Chat Apps     │    │  Agent Bridge   │    │    AI Agents    │
│  (Mattermost,   │◄──►│                 │◄──►│  (Claude Code,  │
│   OpenAI API,   │    │  - Platform API │    │   Codex,        │
│   etc.)         │    │  - Agent API    │    │   OpenCode)     │
└─────────────────┘    │  - Bridge Core  │    └─────────────────┘
                       └─────────────────┘
```

**Message Flow**:

1. User sends a message in the chat application
2. Platform receives the message and converts it to a unified format
3. Bridge processes the message (parses commands, manages sessions)
4. Agent receives the prompt and calls the AI agent
5. AI agent returns results, pushing them one by one through the reply callback
6. Platform sends the results back to the chat application

## 2. Core Concepts

### 2.1 Platform

Platform is an abstraction layer for chat applications, responsible for:

- Receiving messages from chat applications
- Converting messages to the unified `bridge.Message` format
- Calling `HandleFunc` to process messages
- Sending AI agent replies back to the chat application

```go
type Platform interface {
    Name() string
    Serve(ctx context.Context, handle HandleFunc) error
    Send(ctx context.Context, req *bridge.Message) (*bridge.Message, error)
}
```

**HandleFunc Signature**:

```go
type HandleFunc func(ctx context.Context, req *bridge.Message, reply bridge.ReplyFunc) error
```

`reply` is a callback function that is called each time the AI agent produces a response. For chat platforms supporting streaming output, messages can be sent immediately when `reply` is called; for scenarios requiring only final results, messages can be temporarily stored in the callback and used after the handle returns.

### 2.2 Agent

Agent is an abstraction layer for AI agents, responsible for:

- Managing AI agent sessions
- Sending prompts and retrieving responses
- Handling permission requests and user questions
- Retrieving session history and model information

```go
type Agent interface {
    // Model management
    ListModels(ctx context.Context, directory string) ([]types.ModelInfo, error)
    ResolveModel(ctx context.Context, spec, directory string) (types.ModelRef, error)

    // Agent management
    ListAgents(ctx context.Context, directory string) ([]types.AgentInfo, error)

    // Session management
    ListSessions(ctx context.Context, directory string) ([]types.Session, error)
    CreateSession(ctx context.Context, request types.CreateSessionRequest) (*types.Session, error)
    GetSession(ctx context.Context, sessionID string) (*types.Session, error)

    // Message interaction
    Prompt(ctx context.Context, sessionID string, prompt string, opts ...types.PromptOptionFunc) (*types.PromptHandle, error)
    PollMessagesAfter(ctx context.Context, sessionID string, afterCompletedAt float64, output types.MessageOutputOptions) ([]*types.Message, error)

    // Permission and question handling
    ListPendingPermissions(ctx context.Context, sessionID string) ([]types.PermissionRequest, error)
    ReplyPermission(ctx context.Context, sessionID string, requestID string, reply types.PermissionReply) error
    ListPendingQuestions(ctx context.Context, sessionID string) ([]types.QuestionRequest, error)
    ReplyQuestion(ctx context.Context, sessionID string, requestID string, answers [][]string) error
}
```

### 2.3 Bridge

Bridge is the core component, responsible for:

- Parsing user input (regular messages or slash commands)
- Managing session binding relationships
- Handling slash commands (`/new`, `/session`, `/model`, etc.)
- Coordinating interaction between Platform and Agent

## 3. Quick Start

### 3.1 Installation

```bash
# Build from source
git clone https://github.com/gitsang/agent-bridge.git
cd agent-bridge
go build -o agent-bridge ./cmd/agent-bridge

# Or use go install
go install github.com/gitsang/agent-bridge/cmd/agent-bridge@latest
```

### 3.2 Configuration

Create configuration file `config.yaml`:

```yaml
# Logging configuration
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

# Platform configuration
platforms:
  # OpenAI-compatible API (supports any OpenAI SDK client)
  openai-compatible:
    openai-compatible:
      listen: ":24368"

  # Mattermost integration
  mattermost:
    mattermost:
      mode: "websocket" # webhook or websocket
      websocket:
        server_url: "https://mattermost.example.com"
        ws_url: "wss://mattermost.example.com"
        access_token: "your-bot-token"

# AI agent configuration
agent:
  driver: claude # Supported: opencode, codex, claude
  claude:
    command: "claude"
    args:
      - "-p"
      - "--output-format"
      - "stream-json"
      - "--verbose"
    timeout: 30m

# Session storage configuration
conversation:
  store:
    type: "sqlite" # memory, file, sqlite
    path: "data/conversation.db"
    ttl: 24h
    max_items: 1024
```

### 3.3 Startup

```bash
# Start with configuration file
./agent-bridge --config config.yaml

# Or use environment variables
export ANTHROPIC_API_KEY="your-api-key"
./agent-bridge --config config.yaml
```

### 3.4 Testing

Test the OpenAI-compatible API using curl:

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

## 4. Configuration Guide

### 4.1 Platform Configuration

#### 4.1.1 OpenAI-compatible API

Provides standard OpenAI Chat Completions API interface, supporting any OpenAI SDK client.

```yaml
platforms:
  openai-compatible:
    openai-compatible:
      listen: ":24368" # Listen address
```

#### 4.1.2 Mattermost (WebSocket)

Suitable for scenarios where Mattermost cannot directly access Agent Bridge (such as internal network environments), actively connecting to Mattermost via WebSocket.

```yaml
platforms:
  mattermost:
    mattermost:
      mode: "websocket"
      command_prefix: "!" # Command prefix, default "/"
      websocket:
        server_url: "https://mattermost.example.com"
        ws_url: "wss://mattermost.example.com"
        access_token: "bot-access-token"
```

#### 4.1.3 Mattermost (Webhook)

Receives messages through Mattermost's Outgoing Webhook or Slash Command.

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

### 4.2 AI Agent Configuration

#### 4.2.1 Claude Code

Uses Claude Code CLI's non-interactive mode as the AI agent.

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

**Notes**:

- Use `--bare` parameter to skip Claude Code's hooks, plugins, MCP and other features
- Authentication via `ANTHROPIC_API_KEY` environment variable
- Current version does not support `/permission` and `/question` commands

#### 4.2.2 Codex

Uses Codex app-server as the AI agent.

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

Uses OpenCode as the AI agent (default driver).

```yaml
agent:
  driver: opencode
  opencode:
    base_url: "http://127.0.0.1:4096"
    timeout: 10m
    db_path: "/root/.local/share/opencode/opencode.db"
```

### 4.3 Message Output Configuration

You can configure the types of messages returned by the AI agent:

```yaml
agent:
  message_output:
    include:
      - answer # Answer content
      - reasoning # Thinking process
      - action # Tool calls
      - artifact # Code changes
      - diagnostic # Diagnostic information
```

### 4.4 Session Storage Configuration

```yaml
conversation:
  store:
    type: "sqlite" # memory, file, sqlite
    path: "data/conversation.db" # File path (for file and sqlite types)
    ttl: 24h # Session expiration time
    max_items: 1024 # Maximum number of sessions
  message:
    include_user_identity: false # Whether to include user identity in messages
```

### 4.5 Logging and Debugging

Enable debug logging:

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

## 5. Command Reference

Agent Bridge supports the following slash commands:

### 5.1 Session Management

```bash
# Create new session
/new [--model <provider/model|model>] [--agent <name>] [--directory <path>] [--title <title>]

# Session operations
/session attach <agent-session-id>  # Bind to existing session
/session detach                     # Unbind current session
/session current                    # Show current session information
/session list [--directory <path>]  # List sessions
```

### 5.2 Models and Agents

```bash
# Model management
/model set <provider/model|model>  # Set default model
/model list                        # List available models

# Agent management
/agent set <name>  # Set default agent
/agent list        # List available agents
```

### 5.3 Working Directory

```bash
# Set working directory
/directory set <path>
```

### 5.4 Permissions and Questions

```bash
# Permission requests
/permission <once|always|reject> [id|index]

# Question answers
/question [id|index] <answer...>
/question reject [id|index]
```

### 5.5 Help

```bash
/help [new|session|model|agent|directory|permission|question]
```

## 6. TODO List

### 6.1 Platform Support

- [x] OpenAI-compatible API (supports any OpenAI SDK client)
- [x] Mattermost (WebSocket mode)
- [x] Mattermost (Webhook mode)
- [ ] Slack (planned)

### 6.2 AI Agent Support

- [x] OpenCode (default)
- [x] Claude Code
- [x] Codex
- [ ] Other ACP-compatible agents (planned)

### 6.3 Other Features

- [x] Support Message command list
- [x] Support agent multi-turn responses (pushing one by one through reply callback)
- [ ] Support SO/gRPC plugins
- [ ] Support Capability Claim

## 7. Contributing Guide

Welcome to contribute code, report issues, or suggest improvements!

1. Fork the project
2. Create a feature branch: `git checkout -b feat/your-feature`
3. Commit changes: `git commit -m 'feat: Add some feature'`
4. Push the branch: `git push origin feat/your-feature`
5. Create a Pull Request

## 8. License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
