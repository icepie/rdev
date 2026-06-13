package protocol

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// MessageType defines the type of WebSocket message (for text/JSON frames)
type MessageType string

const (
	// Control (text frames)
	MsgRegister   MessageType = "register"    // C->S: register with ID + password
	MsgNewSession MessageType = "new_session" // S->C: create a proxied SSH session

	MsgStdinClose MessageType = "stdin_close" // S->C: remote closed stdin (EOF)
	MsgClose      MessageType = "close"      // bidir: session done, clean up
	MsgResize     MessageType = "resize"     // S->C: terminal resize
	MsgExitCode   MessageType = "exit_code"  // C->S: command exit code

	// TCP port forwarding (text frames for control, binary for data)
	MsgTCPConnect MessageType = "tcp_connect" // S->C: dial a TCP target (for -L)
	MsgTCPOpen    MessageType = "tcp_open"    // C->S: TCP connection established
	MsgTCPFail    MessageType = "tcp_fail"    // C->S: TCP connection failed
	MsgTCPClose   MessageType = "tcp_close"  // bidir: close a TCP connection
	MsgTCPListen  MessageType = "tcp_listen"  // S->C: start TCP listener (for -R via device)
	MsgTCPListenOK MessageType = "tcp_listen_ok" // C->S: listener started
	MsgTCPAccept  MessageType = "tcp_accept"  // C->S: new connection on listener

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
	BinData    byte = 0x01 // Session data (stdin/stdout)
	BinStderr  byte = 0x02 // Session stderr
	BinTCPData byte = 0x03 // TCP forwarding data
	BinFilePut byte = 0x04 // File write to device (extended header)
)

// BinFilePut extended layout after common header:
// [2 bytes pathLen BE] [pathLen bytes: path] [4 bytes mode BE] [file data]

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
	FileMode  int32  `json:"fileMode,omitempty"`
	Success   bool   `json:"success,omitempty"`

	// Legacy fields (text frames)
	Data   string `json:"data,omitempty"`
	Stderr string `json:"stderr,omitempty"`
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

// EncodeBinFilePut encodes a file put binary frame
// Layout: [0x04] [1 idLen] [id] [2 pathLen BE] [path] [4 mode BE] [file data]
func EncodeBinFilePut(id, path string, mode int32, fileData []byte) []byte {
	idb := []byte(id)
	pathb := []byte(path)
	n := 2 + len(idb) + 2 + len(pathb) + 4 + len(fileData)
	buf := make([]byte, n)
	pos := 0
	buf[pos] = BinFilePut; pos++
	buf[pos] = byte(len(idb)); pos++
	copy(buf[pos:], idb); pos += len(idb)
	binary.BigEndian.PutUint16(buf[pos:], uint16(len(pathb))); pos += 2
	copy(buf[pos:], pathb); pos += len(pathb)
	binary.BigEndian.PutUint32(buf[pos:], uint32(mode)); pos += 4
	copy(buf[pos:], fileData)
	return buf
}

// DecodeBinFilePut decodes a file put binary frame's payload fields
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

// Verify binary protocol roundtrip
func init() {
	// Quick sanity check
	data := []byte("hello world")
	frame := EncodeBinFrame(BinData, "session123", data)
	typ, id, payload, err := DecodeBinFrame(frame)
	if err != nil || typ != BinData || id != "session123" || string(payload) != "hello world" {
		panic("binary protocol roundtrip failed")
	}

	// File put roundtrip
	fileFrame := EncodeBinFilePut("id1", "/tmp/test.txt", 0644, []byte("file content"))
	ftyp, fid, fpayload, ferr := DecodeBinFrame(fileFrame)
	if ferr != nil || ftyp != BinFilePut || fid != "id1" {
		panic("file put frame decode failed")
	}
	fpath, fmode, fdata, ferr2 := DecodeBinFilePut(fpayload)
	if ferr2 != nil || fpath != "/tmp/test.txt" || fmode != 0644 || string(fdata) != "file content" {
		panic(fmt.Sprintf("file put payload decode failed: path=%q mode=%d data=%q err=%v", fpath, fmode, string(fdata), ferr2))
	}
}
