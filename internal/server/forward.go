package server

import (
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
	"rdev/internal/protocol"
)

// ForwardedTCPHandler handles device-side -R reverse port forwarding.
type ForwardedTCPHandler struct {
	// Keys are clientID + "\x00" + bind address so one SSH session can never
	// see or cancel another device's reverse forwards.
	forwards map[string]string // clientID\x00bind address -> listen ID
	mu       sync.Mutex
	srv      *Server
}

func forwardKey(clientID, addr string) string {
	return clientID + "\x00" + addr
}

type remoteForwardRequest struct {
	BindAddr string
	BindPort uint32
}

type remoteForwardSuccess struct {
	BindPort uint32
}

type remoteForwardCancelRequest struct {
	BindAddr string
	BindPort uint32
}

type remoteForwardChannelData struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

func (h *ForwardedTCPHandler) HandleSSHRequest(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
	h.mu.Lock()
	if h.forwards == nil {
		h.forwards = make(map[string]string)
	}
	h.mu.Unlock()

	clientID := ctx.User()
	client, ok := h.srv.GetClient(clientID)
	if !ok {
		return false, []byte{}
	}

	switch req.Type {
	case "tcpip-forward":
		var payload remoteForwardRequest
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
			return false, []byte{}
		}

		listenID := generateID()
		addr := net.JoinHostPort(payload.BindAddr, strconv.Itoa(int(payload.BindPort)))
		rev := &ReverseForward{
			ID:       listenID,
			ClientID: clientID,
			BindAddr: payload.BindAddr,
			BindPort: payload.BindPort,
			SSHConn:  ctx.Value(ssh.ContextKeyConn).(*gossh.ServerConn),
			OpenCh:   make(chan struct{}),
			Cancel: func() {
				client.Send(&protocol.Message{Type: protocol.MsgTCPClose, ListenID: listenID})
			},
		}
		h.srv.RegisterReverseForward(rev)
		defer func() {
			if _, errText := rev.Result(); errText != "" {
				h.srv.removeReverseForward(listenID)
			}
		}()

		if err := client.Send(&protocol.Message{
			Type:     protocol.MsgTCPListen,
			ListenID: listenID,
			Host:     payload.BindAddr,
			Port:     int(payload.BindPort),
		}); err != nil {
			h.srv.removeReverseForward(listenID)
			return false, []byte{}
		}

		select {
		case <-rev.OpenCh:
		case <-time.After(10 * time.Second):
			h.srv.removeReverseForward(listenID)
			client.Send(&protocol.Message{Type: protocol.MsgTCPClose, ListenID: listenID})
			return false, []byte{}
		case <-ctx.Done():
			h.srv.removeReverseForward(listenID)
			client.Send(&protocol.Message{Type: protocol.MsgTCPClose, ListenID: listenID})
			return false, []byte{}
		}

		port, errText := rev.Result()
		if errText != "" {
			log.Printf("ssh fwd -R(device): listen %s failed: %s", addr, errText)
			return false, []byte{}
		}
		rev.BindPort = port
		boundAddr := net.JoinHostPort(payload.BindAddr, strconv.Itoa(int(port)))
		h.mu.Lock()
		h.forwards[forwardKey(clientID, addr)] = listenID
		h.forwards[forwardKey(clientID, boundAddr)] = listenID
		h.mu.Unlock()

		go func() {
			<-ctx.Done()
			h.cancel(boundAddr, client)
		}()

		log.Printf("ssh fwd -R(device): listening on %s (listenID=%s)", boundAddr, listenID)
		return true, gossh.Marshal(&remoteForwardSuccess{BindPort: port})

	case "cancel-tcpip-forward":
		var payload remoteForwardCancelRequest
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
			return false, []byte{}
		}
		addr := net.JoinHostPort(payload.BindAddr, strconv.Itoa(int(payload.BindPort)))
		h.cancel(addr, client)
		return true, nil

	default:
		return false, nil
	}
}

func (h *ForwardedTCPHandler) cancel(addr string, client *ClientConn) {
	// Only touch forwards owned by this client: keys are client-scoped.
	prefix := client.ID + "\x00"
	h.mu.Lock()
	listenID := h.forwards[prefix+addr]
	if listenID != "" {
		for key, id := range h.forwards {
			if id == listenID && strings.HasPrefix(key, prefix) {
				delete(h.forwards, key)
			}
		}
	}
	h.mu.Unlock()
	if listenID == "" {
		return
	}
	if fwd := h.srv.getReverseForward(listenID); fwd != nil && fwd.Cancel != nil {
		fwd.Cancel()
	}
	h.srv.removeReverseForward(listenID)
}
