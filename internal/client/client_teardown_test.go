package client

import (
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// closeReporter records whether the wrapped connection was closed.
type closeReporter struct {
	net.Conn
	mu     sync.Mutex
	closed bool
}

func (c *closeReporter) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.Conn.Close()
}

func (c *closeReporter) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// A dropped stream transport must release every connection-scoped resource,
// including legacy file streams that used to survive transport teardown.
func TestStreamTransportTeardownReleasesResources(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	forwardClient, forwardServer := net.Pipe()
	defer forwardServer.Close()

	file, err := os.CreateTemp(t.TempDir(), "teardown")
	if err != nil {
		t.Fatal(err)
	}

	c := NewClient("", "device", "", "")
	transport := &streamClientTransport{conn: clientConn}
	sess := &clientSession{id: "sess-1", done: make(chan struct{})}
	forward := &closeReporter{Conn: forwardClient}

	c.mu.Lock()
	c.transport = transport
	c.sessions[sess.id] = sess
	c.forwards["fwd-1"] = forward
	c.fileStreams["bin-1"] = &fileStream{path: file.Name(), file: file, mode: 0644}
	c.mu.Unlock()

	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		c.streamReadLoop(clientConn, transport)
	}()

	// Drop the peer: the read loop hits EOF and tears the transport down.
	serverConn.Close()

	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("transport teardown did not signal disconnect")
	}
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("read loop did not exit")
	}

	c.mu.Lock()
	sessions, forwards, streams, pending := len(c.sessions), len(c.forwards), len(c.fileStreams), len(c.tcpDials)
	current := c.transport
	c.mu.Unlock()

	if sessions != 0 {
		t.Fatalf("sessions not emptied: %d left", sessions)
	}
	if forwards != 0 {
		t.Fatalf("forwards not emptied: %d left", forwards)
	}
	if streams != 0 {
		t.Fatalf("fileStreams not emptied: %d left", streams)
	}
	if pending != 0 {
		t.Fatalf("tcpDials not emptied: %d left", pending)
	}
	if current != nil {
		t.Fatal("transport still registered after teardown")
	}

	select {
	case <-sess.done:
	default:
		t.Fatal("session was not closed")
	}
	if !forward.Closed() {
		t.Fatal("forward connection was not closed")
	}
	if _, err := file.Stat(); err == nil {
		t.Fatal("file stream handle was not closed")
	}
}
