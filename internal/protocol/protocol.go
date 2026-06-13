package protocol

import (
	"encoding/base64"
	"encoding/json"
)

// MessageType defines the type of WebSocket message
type MessageType string

const (
	// Control
	MsgRegister   MessageType = "register"    // C->S: register with ID + password
	MsgNewSession MessageType = "new_session" // S->C: create a proxied SSH session

	// Session data
	MsgData       MessageType = "data"       // bidir: stdout (base64)
	MsgStderrData MessageType = "stderr"     // bidir: stderr (base64)
	MsgStdinClose MessageType = "stdin_close" // S->C: remote closed stdin (EOF)
	MsgClose      MessageType = "close"      // bidir: session done, clean up
	MsgResize     MessageType = "resize"     // S->C: terminal resize
	MsgExitCode   MessageType = "exit_code"  // C->S: command exit code

	// TCP port forwarding
	MsgTCPConnect  MessageType = "tcp_connect"  // S->C: dial a TCP target (for -L)
	MsgTCPOpen     MessageType = "tcp_open"     // C->S: TCP connection established
	MsgTCPFail     MessageType = "tcp_fail"     // C->S: TCP connection failed
	MsgTCPData     MessageType = "tcp_data"     // bidir: TCP data (base64)
	MsgTCPClose    MessageType = "tcp_close"    // bidir: close a TCP connection
	MsgTCPListen   MessageType = "tcp_listen"   // S->C: start TCP listener (for -R via device)
	MsgTCPListenOK MessageType = "tcp_listen_ok" // C->S: listener started
	MsgTCPAccept   MessageType = "tcp_accept"   // C->S: new connection on listener
)

// Message is the WebSocket protocol message
type Message struct {
	Type      MessageType `json:"type"`
	ClientID  string      `json:"clientId,omitempty"`
	SessionID string      `json:"sessionId,omitempty"`

	// Session creation
	Subsystem string            `json:"subsystem,omitempty"` // "", "sftp"
	Command   string            `json:"command,omitempty"`
	Pty       bool              `json:"pty,omitempty"`
	Env       []string          `json:"env,omitempty"`
	Term      string            `json:"term,omitempty"`
	Rows      int               `json:"rows,omitempty"`
	Cols      int               `json:"cols,omitempty"`
	Modes     map[uint8]uint32  `json:"modes,omitempty"` // SSH terminal modes

	// Session data
	Data   string `json:"data,omitempty"`   // base64 encoded
	Stderr string `json:"stderr,omitempty"` // base64 encoded

	// Auth
	Password string `json:"password,omitempty"`

	// Exit
	ExitCode int `json:"exitCode,omitempty"`

	// TCP forwarding
	ForwardID  string `json:"forwardId,omitempty"`  // unique ID for this forwarded connection
	ListenID   string `json:"listenId,omitempty"`   // unique ID for this listener
	Host       string `json:"host,omitempty"`        // target host
	Port       int    `json:"port,omitempty"`         // target port
	SourceAddr string `json:"sourceAddr,omitempty"`  // origin address for accepted connections
	Error      string `json:"error,omitempty"`        // error message for failures
}

// Encode marshals a message to JSON bytes
func Encode(m *Message) ([]byte, error) {
	return json.Marshal(m)
}

// Decode unmarshals JSON bytes to a message
func Decode(data []byte) (*Message, error) {
	var m Message
	err := json.Unmarshal(data, &m)
	return &m, err
}

// EncodeData base64-encodes raw bytes
func EncodeData(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

// DecodeData base64-decodes a string to raw bytes
func DecodeData(encoded string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(encoded)
}
