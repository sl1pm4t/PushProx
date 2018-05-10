package util

// Define our message object
type SocketMessage struct {
	Type    MessageType       `json:"type"`
	Payload map[string]string `json:"payload"`
}

type MessageType int

const (
	Register MessageType = iota
	Ready
	Request
	Response
	Error
)

func (t MessageType) String() string {
	switch t {
	case Register:
		return "Register"
	case Ready:
		return "Ready"
	case Request:
		return "Request"
	case Response:
		return "Response"
	case Error:
		return "Error"
	}
	return "INVALID"
}
