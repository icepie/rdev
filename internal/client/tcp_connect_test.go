package client

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"rdev/internal/protocol"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTCPConnectDoesNotBlockControlLoopDuringTunnelHandshake(t *testing.T) {
	release := make(chan struct{})
	handshake := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(handshake); <-release; http.Error(w, "test", 500) }))
	defer backend.Close()
	defer close(release)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := NewClient("", "device", "", "")
	defer c.cleanup()
	c.activeWSBase = "ws" + strings.TrimPrefix(backend.URL, "http")
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	returned := make(chan struct{})
	go func() {
		c.handleTCPConnect(&protocol.Message{ForwardID: "slow", Host: "127.0.0.1", Port: port, Tunnel: "/wait"})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("control loop blocked by connect")
	}
	first, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	select {
	case <-handshake:
	case <-time.After(time.Second):
		t.Fatal("no tunnel handshake")
	}
	c.handleTCPConnect(&protocol.Message{ForwardID: "fast", Host: "127.0.0.1", Port: port})
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
	second, err := listener.Accept()
	if err != nil {
		t.Fatal("second dial blocked by first", err)
	}
	second.Close()
	c.handleTCPClose(&protocol.Message{ForwardID: "slow"})
}

func TestTCPCloseCancelsPendingDial(t *testing.T) {
	c := NewClient("", "device", "", "")
	ctx, cancel := context.WithCancel(context.Background())
	c.tcpDials = map[string]*pendingTCPDial{"pending": {ctx: ctx, cancel: cancel}}
	c.handleTCPClose(&protocol.Message{ForwardID: "pending"})
	if ctx.Err() != context.Canceled || len(c.tcpDials) != 0 {
		t.Fatal("pending dial survived close")
	}
}
