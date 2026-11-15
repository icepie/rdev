package client

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"rdev/internal/protocol"
)

func transferDigest(data string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(data))) }

func TestManagedUploadNeverPublishesStalePrefix(t *testing.T) {
	for _, identity := range []string{"", transferDigest("aaaaaaaa"), transferDigest("bbbbbbbb")} {
		t.Run(identity, func(t *testing.T) {
			c, tr := newUploadTestClient()
			path := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil { t.Fatal(err) }
			if err := os.WriteFile(path+".rdevpart", []byte("OLD!"), 0600); err != nil { t.Fatal(err) }
			if identity != "" {
				if err := os.WriteFile(path+".rdevpart.meta", []byte(identity+"\n8\n"), 0600); err != nil { t.Fatal(err) }
			}
			c.handleManagedUploadStart(&protocol.Message{TaskID: "upload", Path: path, Size: 8, SHA256: transferDigest("bbbbbbbb")})
			ready := uploadReply(t, tr)
			if ready.Type != protocol.MsgFileUploadReady { t.Fatal(ready) }
			if identity != transferDigest("bbbbbbbb") && ready.Offset != 0 { t.Fatal("unrelated partial resumed", ready) }
			if ready.PrefixSHA256 != transferDigest(strings.Repeat("b", int(ready.Offset))) && ready.Offset != 0 {
				// A browser rejects this forged/stale binding. Even a caller that
				// ignores prefix verification cannot make the device commit it.
				c.handleManagedUploadChunk("upload", ready.Offset, []byte("bbbbbbbb")[ready.Offset:])
				c.handleManagedUploadEnd(&protocol.Message{TaskID: "upload"})
				if end := uploadReply(t, tr); end.Type != protocol.MsgFileTransferError { t.Fatal(end) }
				got, err := os.ReadFile(path)
				if err != nil || string(got) != "original" { t.Fatal(string(got), err) }
				return
			}
			c.handleManagedUploadChunk("upload", 0, []byte("bbbbbbbb"))
			c.handleManagedUploadEnd(&protocol.Message{TaskID: "upload"})
			if end := uploadReply(t, tr); !end.Success || end.SHA256 != transferDigest("bbbbbbbb") { t.Fatal(end) }
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "bbbbbbbb" { t.Fatal(string(got), err) }
			if _, err := os.Stat(path+".rdevpart.meta"); !os.IsNotExist(err) { t.Fatal("binding leaked", err) }
		})
	}
}

func TestManagedUploadOversizePreservesDestination(t *testing.T) {
	c, tr := newUploadTestClient()
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil { t.Fatal(err) }
	c.handleManagedUploadStart(&protocol.Message{TaskID: "upload", Path: path, Size: 1, SHA256: transferDigest("a")})
	uploadReply(t, tr)
	c.handleManagedUploadChunk("upload", 0, []byte("ab"))
	if reply := uploadReply(t, tr); reply.Type != protocol.MsgFileTransferError { t.Fatal(reply) }
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" { t.Fatal(string(got), err) }
	if len(c.uploads) != 0 || len(c.uploadPaths) != 0 { t.Fatal("failed upload retained lease") }
}

func TestDownloadFingerprintRejectsChangedRange(t *testing.T) {
	c, tr := newUploadTestClient()
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("abcdefgh"), 0600); err != nil { t.Fatal(err) }
	c.handleManagedDownloadStart(&protocol.Message{TaskID: "one", Path: path, Length: 4})
	start := uploadReply(t, tr)
	if start.SHA256 != transferDigest("abcdefgh") { t.Fatal(start) }
	uploadReply(t, tr)
	if err := os.WriteFile(path, []byte("ABCDEFGH"), 0600); err != nil { t.Fatal(err) }
	when := time.Now().Add(time.Second)
	if err := os.Chtimes(path, when, when); err != nil { t.Fatal(err) }
	c.handleManagedDownloadStart(&protocol.Message{TaskID: "two", Path: path, Offset: 4, Length: 4, SHA256: start.SHA256})
	if reply := uploadReply(t, tr); reply.Type != protocol.MsgFileTransferError { t.Fatal("mixed source range accepted", reply) }
}

func TestManagedUploadRestartDiscardsBoundCorruptPrefix(t *testing.T) {
	c, tr := newUploadTestClient()
	path := filepath.Join(t.TempDir(), "target")
	digest := transferDigest("bbbbbbbb")
	if err := os.WriteFile(path+".rdevpart", []byte("OLD!"), 0600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(path+".rdevpart.meta", []byte(digest+"\n8\n"), 0600); err != nil { t.Fatal(err) }
	c.handleManagedUploadStart(&protocol.Message{TaskID: "retry", Path: path, Size: 8, SHA256: digest, Restart: true})
	ready := uploadReply(t, tr)
	if ready.Type != protocol.MsgFileUploadReady || ready.Offset != 0 || ready.PrefixSHA256 != transferDigest("") { t.Fatal(ready) }
	c.handleManagedUploadChunk("retry", 0, []byte("bbbbbbbb"))
	c.handleManagedUploadEnd(&protocol.Message{TaskID: "retry"})
	if reply := uploadReply(t, tr); !reply.Success { t.Fatal(reply) }
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "bbbbbbbb" { t.Fatal(string(got), err) }
}

func TestManagedUploadRequiresSourceHashBeforeTouchingDestination(t *testing.T) {
	c, tr := newUploadTestClient()
	path := filepath.Join(t.TempDir(), "target")
	c.handleManagedUploadStart(&protocol.Message{TaskID: "unverified", Path: path, Size: 8})
	if reply := uploadReply(t, tr); reply.Type != protocol.MsgFileTransferError { t.Fatal(reply) }
	if _, err := os.Stat(path+".rdevpart"); !os.IsNotExist(err) { t.Fatal("unverified upload touched partial", err) }
}

func TestIntegrityScanHonorsBothCancellationSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("content"), 0600); err != nil { t.Fatal(err) }
	f, err := os.Open(path)
	if err != nil { t.Fatal(err) }
	defer f.Close()
	canceled := make(chan struct{})
	close(canceled)
	for _, rtc := range []bool{false, true} {
		var cancel, interrupt <-chan struct{}
		if rtc { interrupt = canceled } else { cancel = canceled }
		if _, err := filePrefixSHA256Cancelable(f, 7, cancel, interrupt); err == nil { t.Fatal("canceled scan succeeded") }
	}
}

func TestDownloadFingerprintCacheBoundsAndExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil { t.Fatal(err) }
	f, err := os.Open(path)
	if err != nil { t.Fatal(err) }
	defer f.Close()
	info, err := f.Stat()
	if err != nil { t.Fatal(err) }
	var cache downloadFingerprintCache
	for i := range 140 {
		got, err := cache.fingerprint(fmt.Sprint(i), f, info, nil)
		if err != nil || got != transferDigest("data") { t.Fatal(got, err) }
	}
	if len(cache.entries) != 128 { t.Fatal("unbounded fingerprint cache", len(cache.entries)) }
	for key, entry := range cache.entries {
		entry.used = time.Now().Add(-6*time.Minute)
		cache.entries[key] = entry
	}
	if _, err := cache.fingerprint("fresh", f, info, nil); err != nil { t.Fatal(err) }
	if len(cache.entries) != 1 { t.Fatal("expired fingerprints retained", len(cache.entries)) }
}

func TestDownloadFingerprintDetectsMutationWithRestoredMtime(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" { t.Skip("change-time metadata unavailable on this platform") }
	c, tr := newUploadTestClient()
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("abcdefgh"), 0600); err != nil { t.Fatal(err) }
	info, err := os.Stat(path)
	if err != nil { t.Fatal(err) }
	c.handleManagedDownloadStart(&protocol.Message{TaskID: "one", Path: path, Length: 4})
	start := uploadReply(t, tr)
	uploadReply(t, tr)
	if err := os.WriteFile(path, []byte("ABCDEFGH"), 0600); err != nil { t.Fatal(err) }
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil { t.Fatal(err) }
	c.handleManagedDownloadStart(&protocol.Message{TaskID: "two", Path: path, Offset: 4, Length: 4, SHA256: start.SHA256})
	if reply := uploadReply(t, tr); reply.Type != protocol.MsgFileTransferError { t.Fatal("timestamp restoration bypassed source identity", reply) }
}
