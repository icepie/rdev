//go:build cgo

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rdev/internal/gui"
	"rdev/internal/server"
)

func init() {
	guiEnabled = true
}

func startGUI(httpAddr string, srv *server.Server) {
	// Wait for HTTP server to be ready before opening browser
	go func() {
		if !gui.WaitForHTTP(httpAddr, 3*time.Second) {
			log.Printf("GUI: HTTP server not ready after 3s")
		}
	}()

	// Device count callback for tray
	deviceCountFn := func() int {
		return srv.ConnectedDeviceCount()
	}

	// onExit: gracefully shut down the server
	onExit := func() {
		log.Printf("GUI: quit requested, shutting down...")
		os.Exit(0)
	}

	// Start systray with timeout fallback
	ch := gui.RunOnce(httpAddr, deviceCountFn, onExit)

	// Handle tray ready/fallback
	go func() {
		ok := <-ch
		if ok {
			log.Printf("GUI: system tray started")
		} else {
			log.Printf("GUI: tray unavailable, opened browser (install StatusNotifierWatcher for tray)")
		}
	}()

	// Handle SIGINT/SIGTERM for clean shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		gui.Quit()
	}()
}
