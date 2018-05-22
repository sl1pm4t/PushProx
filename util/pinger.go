package util

import "golang.org/x/net/websocket"

var PINGER = websocket.Codec{pingerMarshal, pongerUnmarshal}

func pingerMarshal(v interface{}) (msg []byte, payloadType byte, err error) {
	return nil, websocket.PingFrame, nil
}

func pongerUnmarshal(msg []byte, payloadType byte, v interface{}) (err error) {
	// NoOp, we don't need to handle pong responses, as they get handled by the websocket library
	return nil
}
