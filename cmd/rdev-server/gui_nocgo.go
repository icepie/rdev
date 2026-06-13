//go:build !cgo

package main

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"runtime"
	"time"

	"rdev/internal/server"
)

func init() {
	guiEnabled = false
}

func startGUI(httpAddr string, srv *server.Server) {
	// No systray without CGO — just open the browser
	go func() {
		// Wait for HTTP server to be ready
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp", httpAddr, 200*time.Millisecond)
			if err == nil {
				conn.Close()
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		url := "http://" + httpAddr
		openBrowserNoCGO(url)
		log.Printf("GUI: opened browser (system tray requires CGO build)")
	}()
}

func openBrowserNoCGO(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if _, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.Command("xdg-open", url)
		} else if _, err := exec.LookPath("sensible-browser"); err == nil {
			cmd = exec.Command("sensible-browser", url)
		} else {
			fmt.Printf("GUI: open manually: %s\n", url)
			return
		}
	}
	if err := cmd.Start(); err != nil {
		log.Printf("GUI: failed to open browser: %v", err)
	}
}
