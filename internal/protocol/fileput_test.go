package protocol

import "testing"

func TestBinFrameRoundTrip(t *testing.T) {
	data := []byte("hello world")
	frame := EncodeBinFrame(BinData, "session123", data)
	typ, id, payload, err := DecodeBinFrame(frame)
	if err != nil {
		t.Fatalf("DecodeBinFrame error: %v", err)
	}
	if typ != BinData || id != "session123" || string(payload) != string(data) {
		t.Fatalf("got type=0x%02x id=%q payload=%q", typ, id, string(payload))
	}
}

func TestBinFilePutRoundTrip(t *testing.T) {
	id := "test123"
	path := "/tmp/test.txt"
	mode := int32(0644)
	data := []byte("HELLO")

	frame := EncodeBinFilePut(id, path, mode, data)
	typ, fid, payload, err := DecodeBinFrame(frame)
	if err != nil {
		t.Fatalf("DecodeBinFrame error: %v", err)
	}
	if typ != BinFilePut {
		t.Fatalf("expected BinFilePut type 0x%02x, got 0x%02x", BinFilePut, typ)
	}
	if fid != id {
		t.Fatalf("expected id %s, got %s", id, fid)
	}

	p, m, d, err := DecodeBinFilePut(payload)
	if err != nil {
		t.Fatalf("DecodeBinFilePut error: %v", err)
	}
	if p != path || m != mode || string(d) != string(data) {
		t.Fatalf("got path=%s mode=%d data=%s, want path=%s mode=%d data=%s", p, m, string(d), path, mode, string(data))
	}
}

func TestBinFrameOffsetRoundTrip(t *testing.T) {
	payload := []byte("chunk")
	frame := EncodeBinFrameOffset(BinFileDownloadChunk, "task-1", 123456789, payload)
	typ, id, offset, got, err := DecodeBinFrameOffset(frame)
	if err != nil {
		t.Fatalf("DecodeBinFrameOffset error: %v", err)
	}
	if typ != BinFileDownloadChunk || id != "task-1" || offset != 123456789 || string(got) != string(payload) {
		t.Fatalf("got type=0x%02x id=%q offset=%d payload=%q", typ, id, offset, string(got))
	}
}
