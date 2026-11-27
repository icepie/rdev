package transport_test

// Shared-byte-corpus test. The same files under testdata/framing are decoded by
// the Rust client (clients/rdev-client-rs/tests/framing_fixtures.rs) and the
// Android client (FramingFixtures.java); docs/protocol-framing.md describes the
// layout. Keep all three readers of one corpus in sync.

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"rdev/internal/protocol"
	"rdev/internal/transport"
)

const framingFixtureDir = "testdata/framing"

type streamFixture struct {
	name      string
	file      string
	expect    string
	kind      byte
	payload   []byte
	remaining []byte
}

type innerFixture struct {
	name     string
	file     string
	expect   string
	typ      byte
	id       string
	offseted bool
	offset   int64
	payload  []byte
}

func loadFramingFixtures(t *testing.T) ([]streamFixture, []innerFixture) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(framingFixtureDir, "fixtures.tsv"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var streams []streamFixture
	var inners []innerFixture
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch line {
		case "[stream]":
			section = "stream"
			continue
		case "[inner]":
			section = "inner"
			continue
		}
		fields := strings.Split(line, "\t")
		switch section {
		case "stream":
			if len(fields) != 6 {
				t.Fatalf("stream row %q: got %d fields, want 6", line, len(fields))
			}
			fx := streamFixture{name: fields[0], file: fields[1], expect: fields[2]}
			if fx.expect == "ok" {
				fx.kind = parseByte(t, fields[0], fields[3])
				fx.payload = parseHex(t, fields[0], fields[4])
				fx.remaining = parseHex(t, fields[0], fields[5])
			}
			streams = append(streams, fx)
		case "inner":
			if len(fields) != 8 {
				t.Fatalf("inner row %q: got %d fields, want 8", line, len(fields))
			}
			fx := innerFixture{name: fields[0], file: fields[1], expect: fields[2]}
			if fx.expect != "ok" {
				inners = append(inners, fx)
				continue
			}
			fx.typ = parseByte(t, fields[0], fields[3])
			fx.id = fields[4]
			fx.offseted = fields[5] == "1"
			if fx.offseted {
				v, err := strconv.ParseInt(fields[6], 10, 64)
				if err != nil {
					t.Fatalf("inner row %s: bad offset %q: %v", fields[0], fields[6], err)
				}
				fx.offset = v
			}
			fx.payload = parseHex(t, fields[0], fields[7])
			inners = append(inners, fx)
		default:
			t.Fatalf("row outside a section: %q", line)
		}
	}
	if len(streams) == 0 || len(inners) == 0 {
		t.Fatal("fixture corpus is empty")
	}
	return streams, inners
}

func parseByte(t *testing.T, name, text string) byte {
	t.Helper()
	v, err := strconv.ParseUint(text, 10, 8)
	if err != nil {
		t.Fatalf("fixture %s: bad byte %q: %v", name, text, err)
	}
	return byte(v)
}

func parseHex(t *testing.T, name, text string) []byte {
	t.Helper()
	if text == "" {
		return []byte{}
	}
	data, err := hex.DecodeString(text)
	if err != nil {
		t.Fatalf("fixture %s: bad hex: %v", name, err)
	}
	return data
}

func readFixtureBytes(t *testing.T, file string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(framingFixtureDir, file))
	if err != nil {
		t.Fatalf("read fixture %s: %v", file, err)
	}
	return data
}

func TestStreamFrameFixtures(t *testing.T) {
	streams, _ := loadFramingFixtures(t)
	var reader *bytes.Reader
	current := ""
	for i, fx := range streams {
		if fx.file != current {
			reader = bytes.NewReader(readFixtureBytes(t, fx.file))
			current = fx.file
		}
		lastForFile := i == len(streams)-1 || streams[i+1].file != fx.file
		frame, err := transport.ReadFrame(reader)
		if fx.expect != "ok" {
			if err == nil {
				t.Fatalf("%s (%s): expected decode failure, got kind=%d payload=%d bytes",
					fx.name, fx.file, frame.Kind, len(frame.Payload))
			}
			if want := strings.TrimPrefix(fx.expect, "error:"); classifyStreamError(err) != want {
				t.Fatalf("%s (%s): error %v classified as %q, want %q",
					fx.name, fx.file, err, classifyStreamError(err), want)
			}
			if !lastForFile {
				t.Fatalf("%s (%s): failure before the last row for this file", fx.name, fx.file)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s (%s): decode: %v", fx.name, fx.file, err)
		}
		if frame.Kind != fx.kind {
			t.Fatalf("%s (%s): kind=%d, want %d", fx.name, fx.file, frame.Kind, fx.kind)
		}
		if !bytes.Equal(frame.Payload, fx.payload) {
			t.Fatalf("%s (%s): payload %x, want %x", fx.name, fx.file, frame.Payload, fx.payload)
		}
		if lastForFile {
			left := make([]byte, reader.Len())
			_, _ = io.ReadFull(reader, left)
			if !bytes.Equal(left, fx.remaining) {
				t.Fatalf("%s (%s): %d trailing bytes %x, want %x",
					fx.name, fx.file, len(left), left, fx.remaining)
			}
		}
	}
}

func classifyStreamError(err error) string {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case strings.Contains(err.Error(), "too large"):
		return "too_large"
	default:
		return "other"
	}
}

func TestBinaryFrameFixtures(t *testing.T) {
	_, inners := loadFramingFixtures(t)
	for _, fx := range inners {
		raw := readFixtureBytes(t, fx.file)
		if fx.expect != "ok" {
			if _, _, _, _, err := protocol.DecodeBinFrameOffset(raw); err == nil {
				t.Fatalf("%s (%s): offset decoder accepted an invalid frame", fx.name, fx.file)
			}
			continue
		}
		typ, id, payload, err := protocol.DecodeBinFrame(raw)
		if err != nil {
			t.Fatalf("%s (%s): decode: %v", fx.name, fx.file, err)
		}
		if typ != fx.typ {
			t.Fatalf("%s (%s): type=%#x, want %#x", fx.name, fx.file, typ, fx.typ)
		}
		if id != fx.id {
			t.Fatalf("%s (%s): id=%q, want %q", fx.name, fx.file, id, fx.id)
		}
		if fx.offseted {
			ot, oid, offset, opayload, err := protocol.DecodeBinFrameOffset(raw)
			if err != nil {
				t.Fatalf("%s (%s): offset decode: %v", fx.name, fx.file, err)
			}
			if ot != fx.typ || oid != fx.id || offset != fx.offset {
				t.Fatalf("%s (%s): offset frame (%#x, %q, %d), want (%#x, %q, %d)",
					fx.name, fx.file, ot, oid, offset, fx.typ, fx.id, fx.offset)
			}
			if !bytes.Equal(opayload, fx.payload) {
				t.Fatalf("%s (%s): offset payload %x, want %x", fx.name, fx.file, opayload, fx.payload)
			}
			if !bytes.Equal(raw, protocol.EncodeBinFrameOffset(fx.typ, fx.id, fx.offset, fx.payload)) {
				t.Fatalf("%s (%s): fixture bytes differ from EncodeBinFrameOffset", fx.name, fx.file)
			}
			continue
		}
		if !bytes.Equal(payload, fx.payload) {
			t.Fatalf("%s (%s): payload %x, want %x", fx.name, fx.file, payload, fx.payload)
		}
		if !bytes.Equal(raw, protocol.EncodeBinFrame(fx.typ, fx.id, fx.payload)) {
			t.Fatalf("%s (%s): fixture bytes differ from EncodeBinFrame", fx.name, fx.file)
		}
	}
}
