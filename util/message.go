package util

// Define our message object
type SocketMessage struct {
	Type    MessageType       `json:"type"`
	Payload map[string]string `json:"payload"`
}

type MessageType int

const (
	// Register is sent by client to proxy, to register it's FQDN for future Proxy requests
	Register MessageType = iota
	// RegisterAck is sent by proxy to client to accept a Register
	RegisterAck
	// Ready is sent by the client indicating it's ready to receive Proxy scrape requests
	Ready
	// Request is sent by proxy to client with the data of a new proxy scrape request.
	Request
	// Response is used by client when it's supplying the scrape response
	Response
	// Error for all error messages
	Error
)

func (t MessageType) String() string {
	switch t {
	case Register:
		return "Register"
	case RegisterAck:
		return "RegisterAck"
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
