// Package gui provides a minimal system tray GUI mode for rdev.
// When enabled, the server shows a tray icon and auto-opens the web dashboard.
// If the system tray is unavailable (e.g. no StatusNotifierWatcher on i3),
// it gracefully falls back to just opening the browser.
package gui

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"runtime"
	"sync/atomic"
	"time"

	"fyne.io/systray"
)

var ready atomic.Bool
var running atomic.Bool

// Run starts the system tray GUI. Blocks until systray exits.
// onExit is called when the user quits from the tray menu.
func Run(httpAddr string, deviceCountFn func() int, onExit func()) {
	systray.Run(func() {
		setupTray(httpAddr, deviceCountFn)
		running.Store(true)
	}, func() {
		running.Store(false)
		if onExit != nil {
			onExit()
		}
	})
}

// RunOnce is a non-blocking version: starts the tray in a goroutine.
// Returns a channel that receives true when tray is ready, or false on timeout.
func RunOnce(httpAddr string, deviceCountFn func() int, onExit func()) <-chan bool {
	ch := make(chan bool, 1)
	go func() {
		systray.Run(func() {
			setupTray(httpAddr, deviceCountFn)
			running.Store(true)
			ch <- true
		}, func() {
			running.Store(false)
			if onExit != nil {
				onExit()
			}
		})
	}()

	// Timeout fallback: if tray doesn't start within 3s, just open browser
	go func() {
		select {
		case <-ch:
			return // tray started successfully
		case <-time.After(3 * time.Second):
			ch <- false
		}
	}()

	return ch
}

func setupTray(httpAddr string, deviceCountFn func() int) {
	systray.SetIcon(iconData)
	systray.SetTitle("RDev")
	systray.SetTooltip("RDev Remote Debug Server")

	// Open Dashboard
	mOpen := systray.AddMenuItem("Open Dashboard", "Open web dashboard in browser")

	// Device count
	mDevices := systray.AddMenuItem("Devices: 0", "Connected devices")
	mDevices.Disable()

	systray.AddSeparator()

	// Quit
	mQuit := systray.AddMenuItem("Quit", "Stop the server and quit")

	ready.Store(true)

	url := fmt.Sprintf("http://%s", httpAddr)

	// Auto-open dashboard on startup
	go openBrowser(url)

	// Main event loop
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-mOpen.ClickedCh:
			openBrowser(url)

		case <-mQuit.ClickedCh:
			systray.Quit()
			return

		case <-ticker.C:
			n := deviceCountFn()
			mDevices.SetTitle(fmt.Sprintf("Devices: %d", n))
			systray.SetTooltip(fmt.Sprintf("RDev — %d device(s) connected", n))
		}
	}
}

// IsReady returns true if the tray has been initialized.
func IsReady() bool {
	return ready.Load()
}

// IsRunning returns true if the tray is currently running.
func IsRunning() bool {
	return running.Load()
}

// Quit exits the systray loop.
func Quit() {
	if running.Load() {
		systray.Quit()
	}
}

// openBrowser opens a URL in the system default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux, bsd
		if _, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.Command("xdg-open", url)
		} else if _, err := exec.LookPath("sensible-browser"); err == nil {
			cmd = exec.Command("sensible-browser", url)
		} else {
			log.Printf("GUI: no browser found, open manually: %s", url)
			return
		}
	}
	if err := cmd.Start(); err != nil {
		log.Printf("GUI: failed to open browser: %v", err)
	}
}

// WaitForHTTP polls until the HTTP server is accepting connections.
func WaitForHTTP(addr string, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
