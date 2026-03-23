package connect

type Message struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
	Workdir   string `json:"workdir,omitempty"`
	Command   string `json:"command,omitempty"`
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
