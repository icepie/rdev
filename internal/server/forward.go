package server

import (
	"io"
	"log"
	"net"
	"strconv"
	"sync"

	gossh "golang.org/x/crypto/ssh"

	"github.com/gliderlabs/ssh"
)

// ForwardedTCPHandler handles -R (reverse) port forwarding.
// It uses the standard SSH tcpip-forward mechanism where the SSH client
// requests the server to listen on a port, and incoming connections are
// forwarded back to the SSH client through a forwarded-tcpip channel.
// This does NOT go through the rdev-client WebSocket — it's between
// the SSH server and the SSH client (debugger) directly.
type ForwardedTCPHandler struct {
	forwards map[string]net.Listener
	mu       sync.Mutex
}

// remoteForwardRequest per RFC4254
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

// HandleSSHRequest handles tcpip-forward and cancel-tcpip-forward global requests
func (h *ForwardedTCPHandler) HandleSSHRequest(ctx ssh.Context, srv *ssh.Server, req *gossh.Request) (bool, []byte) {
	h.mu.Lock()
	if h.forwards == nil {
		h.forwards = make(map[string]net.Listener)
	}
	h.mu.Unlock()

	conn := ctx.Value(ssh.ContextKeyConn).(*gossh.ServerConn)

	switch req.Type {
	case "tcpip-forward":
		var payload remoteForwardRequest
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
			return false, []byte{}
		}

		addr := net.JoinHostPort(payload.BindAddr, strconv.Itoa(int(payload.BindPort)))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Printf("ssh fwd -R: listen %s failed: %v", addr, err)
			return false, []byte{}
		}

		_, destPortStr, _ := net.SplitHostPort(ln.Addr().String())
		destPort, _ := strconv.Atoi(destPortStr)

		h.mu.Lock()
		h.forwards[addr] = ln
		h.mu.Unlock()

		// Close listener when SSH connection closes
		go func() {
			<-ctx.Done()
			h.mu.Lock()
			if l, ok := h.forwards[addr]; ok {
				l.Close()
				delete(h.forwards, addr)
			}
			h.mu.Unlock()
		}()

		// Accept connections and forward back to SSH client
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					break
				}
				originAddr, originPortStr, _ := net.SplitHostPort(c.RemoteAddr().String())
				originPort, _ := strconv.Atoi(originPortStr)

				channelPayload := gossh.Marshal(&remoteForwardChannelData{
					DestAddr:   payload.BindAddr,
					DestPort:   uint32(destPort),
					OriginAddr: originAddr,
					OriginPort: uint32(originPort),
				})

				go func() {
					ch, reqs, err := conn.OpenChannel("forwarded-tcpip", channelPayload)
					if err != nil {
						log.Printf("ssh fwd -R: open channel failed: %v", err)
						c.Close()
						return
					}
					go gossh.DiscardRequests(reqs)

					go func() {
						defer ch.Close()
						defer c.Close()
						copyZeroDst(ch, c)
					}()
					go func() {
						defer ch.Close()
						defer c.Close()
						copyZeroDst(c, ch)
					}()
				}()
			}
		}()

		log.Printf("ssh fwd -R: listening on %s (port %d)", addr, destPort)
		return true, gossh.Marshal(&remoteForwardSuccess{uint32(destPort)})

	case "cancel-tcpip-forward":
		var payload remoteForwardCancelRequest
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
			return false, []byte{}
		}
		addr := net.JoinHostPort(payload.BindAddr, strconv.Itoa(int(payload.BindPort)))
		h.mu.Lock()
		if ln, ok := h.forwards[addr]; ok {
			ln.Close()
			delete(h.forwards, addr)
		}
		h.mu.Unlock()
		return true, nil

	default:
		return false, nil
	}
}

// copyZeroDst copies from src to dst, used for TCP forwarding
func copyZeroDst(dst io.Writer, src io.Reader) {
	_, _ = io.Copy(dst, src)
}
