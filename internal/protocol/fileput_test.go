package protocol

import (
	"testing"
)

func TestBinFilePutRoundTrip(t *testing.T) {
	id := "test123"
	path := "/tmp/test.txt"
	mode := int32(420)
	data := []byte("HELLO")
	
	// Encode full frame
	frame := EncodeBinFilePut(id, path, mode, data)
	t.Logf("Encoded frame: len=%d first_byte=0x%02x", len(frame), frame[0])
	
	// Decode through BinFrame first (as the client does)
	typ, fid, payload, err := DecodeBinFrame(frame)
	if err != nil {
		t.Fatalf("DecodeBinFrame error: %v", err)
	}
	if typ != BinFilePut {
		t.Fatalf("Expected BinFilePut type 0x%02x, got 0x%02x", BinFilePut, typ)
	}
	if fid != id {
		t.Fatalf("Expected id %s, got %s", id, fid)
	}
	
	// Then decode payload
	p, m, d, err := DecodeBinFilePut(payload)
	if err != nil {
		t.Fatalf("DecodeBinFilePut error: %v", err)
	}
	if p != path || m != mode || string(d) != string(data) {
		t.Fatalf("Mismatch: got path=%s mode=%d data=%s, want path=%s mode=%d data=%s", 
			p, m, string(d), path, mode, string(data))
	}
	t.Logf("OK: id=%s path=%s mode=%d data=%s", id, p, m, string(d))
}
