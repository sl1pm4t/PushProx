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
