package bridge

type ReplyFunc func(msg *Message) error

type Message struct {
	Content string       `json:"content"`
	Chat    ChatContext  `json:"chat,omitzero"`
	Agent   AgentContext `json:"agent,omitzero"`
}

type ChatContext struct {
	SessionID string `json:"session_id,omitempty"`
}

type AgentContext struct {
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Model     string `json:"model,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Workdir   string `json:"workdir,omitempty"`
}

type Error struct {
	StatusCode int
	Message    string
}

func (e *Error) Error() string {
	return e.Message
}

func NewError(statusCode int, message string) *Error {
	return &Error{StatusCode: statusCode, Message: message}
}
