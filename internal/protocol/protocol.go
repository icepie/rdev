package protocol

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
)

// MessageType defines the type of WebSocket message (for text/JSON frames)
type MessageType string

const (
	// Control (text frames)
	MsgRegister   MessageType = "register"    // C->S: register with ID + password
	MsgNewSession MessageType = "new_session" // S->C: create a proxied SSH session

	MsgStdinClose MessageType = "stdin_close" // S->C: remote closed stdin (EOF)
	MsgClose      MessageType = "close"       // bidir: session done, clean up
	MsgResize     MessageType = "resize"      // S->C: terminal resize
	MsgExitCode   MessageType = "exit_code"   // C->S: command exit code

	// TCP port forwarding (text frames for control, binary for data)
	MsgTCPConnect  MessageType = "tcp_connect"   // S->C: dial a TCP target (for -L)
	MsgTCPOpen     MessageType = "tcp_open"      // C->S: TCP connection established
	MsgTCPFail     MessageType = "tcp_fail"      // C->S: TCP connection failed
	MsgTCPClose    MessageType = "tcp_close"     // bidir: close a TCP connection
	MsgTCPListen   MessageType = "tcp_listen"    // S->C: start TCP listener (for -R via device)
	MsgTCPListenOK MessageType = "tcp_listen_ok" // C->S: listener started
	MsgTCPAccept   MessageType = "tcp_accept"    // C->S: new connection on listener

	// Session management (S<->Browser)
	MsgSessionList   MessageType = "session_list"   // S->Browser: list active sessions
	MsgSessionAttach MessageType = "session_attach" // Browser->S: attach to a session

	// File distribution (text frames for control, binary for data)
	MsgFileResult MessageType = "file_result" // C->S: file write result {success, error}

	// Legacy text-frame data types (kept for reference, use binary frames instead)
	MsgData       MessageType = "data"
	MsgStderrData MessageType = "stderr"
	MsgTCPData    MessageType = "tcp_data"
	MsgFilePut    MessageType = "file_put"
)

// Binary frame types for high-performance data transfer (OpcodeBinary frames)
// Layout: [1 byte type] [1 byte idLen] [idLen bytes: session/forward ID] [payload]
const (
	BinData      byte = 0x01 // Session data (stdin/stdout)
	BinStderr    byte = 0x02 // Session stderr
	BinTCPData   byte = 0x03 // TCP forwarding data
	BinFilePut   byte = 0x04 // File write to device (single-frame payload)
	BinFileStart byte = 0x05 // File write stream start (extended header)
	BinFileChunk byte = 0x06 // File write stream chunk
	BinFileEnd   byte = 0x07 // File write stream end
	BinFileAck   byte = 0x08 // File write stream chunk acknowledged
)

// File binary extended layout after common header:
// [2 bytes pathLen BE] [pathLen bytes: path] [4 bytes mode BE] [optional file data]

// Message is the WebSocket protocol message (for text/JSON frames)
type Message struct {
	Type      MessageType `json:"type"`
	ClientID  string      `json:"clientId,omitempty"`
	SessionID string      `json:"sessionId,omitempty"`

	// Session creation
	Subsystem string           `json:"subsystem,omitempty"` // "", "sftp"
	Command   string           `json:"command,omitempty"`
	Pty       bool             `json:"pty,omitempty"`
	Env       []string         `json:"env,omitempty"`
	Term      string           `json:"term,omitempty"`
	Rows      int              `json:"rows,omitempty"`
	Cols      int              `json:"cols,omitempty"`
	Modes     map[uint8]uint32 `json:"modes,omitempty"` // SSH terminal modes

	// Auth
	Password string `json:"password,omitempty"`

	// Server info (S->C in MsgRegister response)
	SSHPort  string `json:"sshPort,omitempty"`  // e.g. "8422"
	HTTPHost string `json:"httpHost,omitempty"` // e.g. "1.2.3.4:8080"

	// Exit
	ExitCode int `json:"exitCode,omitempty"`

	// TCP forwarding
	ForwardID  string `json:"forwardId,omitempty"`
	ListenID   string `json:"listenId,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	SourceAddr string `json:"sourceAddr,omitempty"`
	Error      string `json:"error,omitempty"`

	// File distribution
	FilePath string `json:"filePath,omitempty"`
	FileMode int32  `json:"fileMode,omitempty"`
	Success  bool   `json:"success,omitempty"`

	// Session management
	SessionType string        `json:"sessionType,omitempty"` // "shell", "exec", "sftp"
	AttachMode  string        `json:"attachMode,omitempty"`  // "monitor" (read-only) or "takeover" (read-write)
	Sessions    []SessionInfo `json:"sessions,omitempty"`

	// Legacy fields (text frames)
	Data   string `json:"data,omitempty"`
	Stderr string `json:"stderr,omitempty"`
}

// SessionInfo describes an active session for the management API.
type SessionInfo struct {
	ID         string `json:"id"`
	ClientID   string `json:"clientId"`
	Type       string `json:"type"`    // "shell", "exec", "sftp"
	Command    string `json:"command"` // for exec
	Pty        bool   `json:"pty"`
	Term       string `json:"term"`
	Rows       int    `json:"rows"`
	Cols       int    `json:"cols"`
	CreatedAt  string `json:"createdAt"`
	HasMonitor bool   `json:"hasMonitor"` // true if someone is monitoring
	HasControl bool   `json:"hasControl"` // true if someone has takeover
}

// --- JSON encoding (for text frames) ---

func Encode(m *Message) ([]byte, error) {
	return json.Marshal(m)
}

func Decode(data []byte) (*Message, error) {
	var m Message
	err := json.Unmarshal(data, &m)
	return &m, err
}

func EncodeData(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func DecodeData(encoded string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(encoded)
}

// --- Binary encoding (for OpcodeBinary frames) ---

// EncodeBinFrame encodes a binary data frame
// Layout: [1 byte type] [1 byte idLen] [idLen bytes ID] [payload]
func EncodeBinFrame(typ byte, id string, payload []byte) []byte {
	idb := []byte(id)
	buf := make([]byte, 2+len(idb)+len(payload))
	buf[0] = typ
	buf[1] = byte(len(idb))
	copy(buf[2:], idb)
	copy(buf[2+len(idb):], payload)
	return buf
}

// DecodeBinFrame decodes a binary data frame
func DecodeBinFrame(raw []byte) (typ byte, id string, payload []byte, err error) {
	if len(raw) < 2 {
		return 0, "", nil, io.ErrUnexpectedEOF
	}
	typ = raw[0]
	idLen := int(raw[1])
	if len(raw) < 2+idLen {
		return 0, "", nil, io.ErrUnexpectedEOF
	}
	id = string(raw[2 : 2+idLen])
	payload = raw[2+idLen:]
	return
}

// EncodeBinFilePut encodes a single-frame file put binary frame.
// Layout: [0x04] [1 idLen] [id] [2 pathLen BE] [path] [4 mode BE] [file data]
func EncodeBinFilePut(id, path string, mode int32, fileData []byte) []byte {
	buf, pos := encodeBinFileHeader(BinFilePut, id, path, mode, len(fileData))
	copy(buf[pos:], fileData)
	return buf
}

// EncodeBinFileStart encodes the first frame of a streamed file write.
func EncodeBinFileStart(id, path string, mode int32) []byte {
	buf, _ := encodeBinFileHeader(BinFileStart, id, path, mode, 0)
	return buf
}

func encodeBinFileHeader(typ byte, id, path string, mode int32, extra int) ([]byte, int) {
	idb := []byte(id)
	pathb := []byte(path)
	n := 2 + len(idb) + 2 + len(pathb) + 4 + extra
	buf := make([]byte, n)
	pos := 0
	buf[pos] = typ
	pos++
	buf[pos] = byte(len(idb))
	pos++
	copy(buf[pos:], idb)
	pos += len(idb)
	binary.BigEndian.PutUint16(buf[pos:], uint16(len(pathb)))
	pos += 2
	copy(buf[pos:], pathb)
	pos += len(pathb)
	binary.BigEndian.PutUint32(buf[pos:], uint32(mode))
	pos += 4
	return buf, pos
}

// DecodeBinFilePut decodes a file header followed by optional data.
func DecodeBinFilePut(payload []byte) (path string, mode int32, fileData []byte, err error) {
	if len(payload) < 2 {
		return "", 0, nil, io.ErrUnexpectedEOF
	}
	pathLen := int(binary.BigEndian.Uint16(payload[:2]))
	if len(payload) < 2+pathLen+4 {
		return "", 0, nil, io.ErrUnexpectedEOF
	}
	path = string(payload[2 : 2+pathLen])
	mode = int32(binary.BigEndian.Uint32(payload[2+pathLen : 2+pathLen+4]))
	fileData = payload[2+pathLen+4:]
	return
}

// BinFrameHeaderLen returns the header length for a binary frame with given id
func BinFrameHeaderLen(id string) int {
	return 2 + len(id)
}

// MustDecodeBinFrame is like DecodeBinFrame but panics on error (for testing)
func MustDecodeBinFrame(raw []byte) (byte, string, []byte) {
	typ, id, payload, err := DecodeBinFrame(raw)
	if err != nil {
		panic(err)
	}
	return typ, id, payload
}
