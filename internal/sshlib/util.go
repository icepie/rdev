package ssh

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"

	gossh "golang.org/x/crypto/ssh"
)

func generateSigner() (gossh.Signer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return gossh.NewSignerFromKey(key)
}

func parsePtyRequest(s []byte) (pty Pty, ok bool) {
	term, s, ok := parseString(s)
	if !ok {
		return
	}
	width32, s, ok := parseUint32(s)
	if !ok {
		return
	}
	height32, s, ok := parseUint32(s)
	if !ok {
		return
	}
	// Skip pixel width (4 bytes) and pixel height (4 bytes)
	_, s, ok = parseUint32(s)
	if !ok {
		return
	}
	_, s, ok = parseUint32(s)
	if !ok {
		return
	}
	// Parse terminal modes (Modelist)
	modesStr, _, ok := parseString(s)
	if ok && len(modesStr) > 0 {
		pty.Modes = parseTerminalModes([]byte(modesStr))
	}
	pty = Pty{
		Term: term,
		Window: Window{
			Width:  int(width32),
			Height: int(height32),
		},
		Modes: pty.Modes,
	}
	return
}

func parseWinchRequest(s []byte) (win Window, ok bool) {
	width32, s, ok := parseUint32(s)
	if width32 < 1 {
		ok = false
	}
	if !ok {
		return
	}
	height32, _, ok := parseUint32(s)
	if height32 < 1 {
		ok = false
	}
	if !ok {
		return
	}
	win = Window{
		Width:  int(width32),
		Height: int(height32),
	}
	return
}

func parseString(in []byte) (out string, rest []byte, ok bool) {
	if len(in) < 4 {
		return
	}
	length := binary.BigEndian.Uint32(in)
	if uint32(len(in)) < 4+length {
		return
	}
	out = string(in[4 : 4+length])
	rest = in[4+length:]
	ok = true
	return
}

func parseUint32(in []byte) (uint32, []byte, bool) {
	if len(in) < 4 {
		return 0, nil, false
	}
	return binary.BigEndian.Uint32(in), in[4:], true
}

// parseTerminalModes decodes RFC 4254 Section 8 terminal mode TLV pairs.
// Each pair is: 1 byte opcode + 4 bytes uint32 value.
// Terminated by opcode 0 (TTY_OP_END).
func parseTerminalModes(data []byte) gossh.TerminalModes {
	modes := make(gossh.TerminalModes)
	for len(data) >= 5 {
		opcode := data[0]
		if opcode == 0 { // TTY_OP_END
			break
		}
		value := binary.BigEndian.Uint32(data[1:5])
		modes[opcode] = value
		data = data[5:]
	}
	return modes
}
