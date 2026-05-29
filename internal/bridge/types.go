package bridge

type ReplyFunc func(msg *Message) error

type ChatContext struct {
	SessionID string `json:"session_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	UserName  string `json:"user_name,omitempty"`
}

type AgentContext struct {
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Model     string `json:"model,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Directory string `json:"directory,omitempty"`
}

type Message struct {
	Content string       `json:"content"`
	Chat    ChatContext  `json:"chat,omitzero"`
	Agent   AgentContext `json:"agent,omitzero"`
}
