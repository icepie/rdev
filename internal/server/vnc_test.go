package server

import (
	"bufio"
	"encoding/binary"
	"image"
	"image/color"
	"io"
	"net"
	"testing"
)

func TestVNCAuthenticateUsernameDevicePassword(t *testing.T) {
	s := NewServer()
	s.clients["pc"] = &ClientConn{ID: "pc", Password: "secret"}
	v := &vncConn{srv: s}

	client, id, ok := v.authenticate("pc", "secret")
	if !ok || client == nil || id != "pc" {
		t.Fatalf("auth failed: ok=%v id=%q client=%#v", ok, id, client)
	}
	if _, _, ok := v.authenticate("pc", "wrong"); ok {
		t.Fatal("wrong password authenticated")
	}
	if _, _, ok := v.authenticate("", "pc"); ok {
		t.Fatal("password-only mode must not authenticate password-protected devices")
	}
}

func TestVNCAuthenticateNoPasswordDeviceFallbacks(t *testing.T) {
	s := NewServer()
	s.clients["open"] = &ClientConn{ID: "open"}
	v := &vncConn{srv: s}

	for _, tc := range []struct{ user, pass string }{{"open", ""}, {"open", "open"}, {"", "open"}} {
		client, id, ok := v.authenticate(tc.user, tc.pass)
		if !ok || client == nil || id != "open" {
			t.Fatalf("auth user=%q pass=%q failed: ok=%v id=%q", tc.user, tc.pass, ok, id)
		}
	}
	if _, _, ok := v.authenticate("open", "other"); ok {
		t.Fatal("unexpected password authenticated for no-password device")
	}
}

func TestVNCHandshakeVeNCryptPlain(t *testing.T) {
	s := NewServer()
	s.clients["pc"] = &ClientConn{ID: "pc", Password: "secret"}
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	v := &vncConn{srv: s, conn: serverConn, reader: bufio.NewReader(serverConn)}
	done := make(chan error, 1)
	go func() { done <- v.handshake() }()

	clientReader := bufio.NewReader(clientConn)
	serverVersion := make([]byte, 12)
	if _, err := io.ReadFull(clientReader, serverVersion); err != nil {
		t.Fatal(err)
	}
	if string(serverVersion) != "RFB 003.008\n" {
		t.Fatalf("server version = %q", serverVersion)
	}
	if _, err := clientConn.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatal(err)
	}
	securityTypes := make([]byte, 2)
	if _, err := io.ReadFull(clientReader, securityTypes); err != nil {
		t.Fatal(err)
	}
	if securityTypes[0] != 1 || securityTypes[1] != rfbSecurityVeNCrypt {
		t.Fatalf("security types = %v", securityTypes)
	}
	if _, err := clientConn.Write([]byte{rfbSecurityVeNCrypt}); err != nil {
		t.Fatal(err)
	}
	vencryptVersion := make([]byte, 2)
	if _, err := io.ReadFull(clientReader, vencryptVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := clientConn.Write(vencryptVersion); err != nil {
		t.Fatal(err)
	}
	status, err := clientReader.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	if status != 0 {
		t.Fatalf("VeNCrypt version status = %d", status)
	}
	count, err := clientReader.ReadByte()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("subtype count = %d", count)
	}
	subtype := readTestU32(t, clientReader)
	if subtype != rfbVeNCryptPlain {
		t.Fatalf("subtype = %d", subtype)
	}
	writeTestU32(t, clientConn, rfbVeNCryptPlain)
	writePlainCredentials(t, clientConn, "pc", "secret")
	if result := readTestU32(t, clientReader); result != 0 {
		t.Fatalf("security result = %d", result)
	}
	if err := <-done; err != nil {
		t.Fatalf("handshake error: %v", err)
	}
}

func TestVNCHandshakeRejectsWrongPassword(t *testing.T) {
	s := NewServer()
	s.clients["pc"] = &ClientConn{ID: "pc", Password: "secret"}
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	v := &vncConn{srv: s, conn: serverConn, reader: bufio.NewReader(serverConn)}
	done := make(chan error, 1)
	go func() { done <- v.handshake() }()

	clientReader := bufio.NewReader(clientConn)
	_, _ = io.CopyN(io.Discard, clientReader, 12)
	_, _ = clientConn.Write([]byte("RFB 003.008\n"))
	_, _ = io.CopyN(io.Discard, clientReader, 2)
	_, _ = clientConn.Write([]byte{rfbSecurityVeNCrypt})
	version := make([]byte, 2)
	_, _ = io.ReadFull(clientReader, version)
	_, _ = clientConn.Write(version)
	_, _ = clientReader.ReadByte()
	_, _ = clientReader.ReadByte()
	_ = readTestU32(t, clientReader)
	writeTestU32(t, clientConn, rfbVeNCryptPlain)
	writePlainCredentials(t, clientConn, "pc", "wrong")
	if result := readTestU32(t, clientReader); result != 1 {
		t.Fatalf("security result = %d, want failure", result)
	}
	length := readTestU32(t, clientReader)
	if length == 0 {
		t.Fatal("expected failure reason")
	}
	_, _ = io.CopyN(io.Discard, clientReader, int64(length))
	if err := <-done; err == nil {
		t.Fatal("expected handshake error")
	}
}

func writePlainCredentials(t *testing.T, w io.Writer, username, password string) {
	t.Helper()
	writeTestU32(t, w, uint32(len(username)))
	writeTestU32(t, w, uint32(len(password)))
	if _, err := io.WriteString(w, username); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, password); err != nil {
		t.Fatal(err)
	}
}

func readTestU32(t *testing.T, r io.Reader) uint32 {
	t.Helper()
	var value uint32
	if err := binary.Read(r, binary.BigEndian, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func writeTestU32(t *testing.T, w io.Writer, value uint32) {
	t.Helper()
	if err := binary.Write(w, binary.BigEndian, value); err != nil {
		t.Fatal(err)
	}
}

func TestRFBKeyMapping(t *testing.T) {
	for _, tc := range []struct {
		keysym uint32
		code   string
	}{{'a', "KeyA"}, {'Z', "KeyZ"}, {'7', "Digit7"}, {0xff0d, "Enter"}, {0xffc2, "F5"}} {
		_, code := rfbKey(tc.keysym)
		if code != tc.code {
			t.Fatalf("keysym %#x code = %q, want %q", tc.keysym, code, tc.code)
		}
	}
}

func TestVNCRawFrameBGRX(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	img.SetRGBA(1, 0, color.RGBA{R: 4, G: 5, B: 6, A: 255})
	frame := vncRawFrame(img, 2, 1)
	want := []byte{3, 2, 1, 0, 6, 5, 4, 0}
	if string(frame) != string(want) {
		t.Fatalf("frame = %v, want %v", frame, want)
	}
}

func TestVNCChangedRect(t *testing.T) {
	prev := make([]byte, 4*3*4)
	next := append([]byte(nil), prev...)
	setTestPixel(next, 4, 1, 1, []byte{1, 2, 3, 0})
	setTestPixel(next, 4, 2, 2, []byte{4, 5, 6, 0})
	x, y, w, h, changed := vncChangedRect(prev, next, 4, 3)
	if !changed || x != 1 || y != 1 || w != 2 || h != 2 {
		t.Fatalf("changed rect = (%d,%d %dx%d changed=%v), want (1,1 2x2 true)", x, y, w, h, changed)
	}
	_, _, _, _, changed = vncChangedRect(next, next, 4, 3)
	if changed {
		t.Fatal("identical frames reported as changed")
	}
}

func TestVNCRawSubrect(t *testing.T) {
	frame := make([]byte, 4*3*4)
	setTestPixel(frame, 4, 1, 1, []byte{1, 2, 3, 0})
	setTestPixel(frame, 4, 2, 1, []byte{4, 5, 6, 0})
	setTestPixel(frame, 4, 1, 2, []byte{7, 8, 9, 0})
	setTestPixel(frame, 4, 2, 2, []byte{10, 11, 12, 0})
	got := vncRawSubrect(frame, 4, 1, 1, 2, 2)
	want := []byte{1, 2, 3, 0, 4, 5, 6, 0, 7, 8, 9, 0, 10, 11, 12, 0}
	if string(got) != string(want) {
		t.Fatalf("subrect = %v, want %v", got, want)
	}
}

func TestVNCStreamKeySharesEquivalentRequests(t *testing.T) {
	request := defaultVNCDesktopRequest()
	request.FPS = 0
	request.Quality = 0
	request.Width = 0
	request.Height = 0
	normalized := defaultVNCDesktopRequest()
	normalized.FPS = 4
	normalized.Quality = 50
	if vncStreamKey("pc", request) != vncStreamKey("pc", normalized) {
		t.Fatal("equivalent normalized VNC requests should share one stream")
	}
}

func TestVNCEncodeZRLECompressesRawTile(t *testing.T) {
	frame := []byte{3, 2, 1, 0, 6, 5, 4, 0}
	encoded := vncEncodeZRLE(frame, 2, 1)
	if len(encoded) == 0 || len(encoded) >= len(frame)+32 {
		t.Fatalf("unexpected ZRLE size %d", len(encoded))
	}
}

func TestVNCEncodeZRLETileUsesPalette(t *testing.T) {
	frame := make([]byte, 4*2*4)
	setTestPixel(frame, 4, 0, 0, []byte{1, 2, 3, 0})
	setTestPixel(frame, 4, 1, 0, []byte{1, 2, 3, 0})
	setTestPixel(frame, 4, 0, 1, []byte{4, 5, 6, 0})
	setTestPixel(frame, 4, 1, 1, []byte{4, 5, 6, 0})
	got := vncEncodeZRLETile(frame, 4, 0, 0, 2, 2)
	want := []byte{2, 1, 2, 3, 4, 5, 6, 0x00, 0xC0}
	if string(got) != string(want) {
		t.Fatalf("palette tile = %v, want %v", got, want)
	}
}

func setTestPixel(frame []byte, width, x, y int, px []byte) {
	off := (y*width + x) * 4
	copy(frame[off:off+4], px)
}
