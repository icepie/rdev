package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"rdev/internal/client"
)

func main() {
	var (
		serverURL string
		clientID  string
		password  string
		shell     string
	)

	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--server", "-s":
			if i+1 < len(os.Args) {
				serverURL = os.Args[i+1]
				i++
			}
		case "--id", "-i":
			if i+1 < len(os.Args) {
				clientID = os.Args[i+1]
				i++
			}
		case "--password", "-p":
			if i+1 < len(os.Args) {
				password = os.Args[i+1]
				i++
			}
		case "--shell", "-S":
			if i+1 < len(os.Args) {
				shell = os.Args[i+1]
				i++
			}
		case "--help":
			fmt.Print(`Usage: rdev-client -s <server-url> [options]

Options:
  --server, -s    Server URL (required, e.g. ws://1.2.3.4:8080)
  --id, -i        Client/Device ID (default: hostname)
  --password, -p   Password for SSH auth (optional, enables password login)
  --shell, -S     Shell to use (default: $SHELL or /bin/sh or cmd.exe)

The shell is used for:
  - Interactive SSH sessions (when no command is specified)
  - Executing SSH remote commands (with -c flag)
  - SCP/SFTP operations

Examples:
  rdev-client -s ws://1.2.3.4:8080 -i mydevice -p secret123
  rdev-client -s ws://1.2.3.4:8080 -i mydevice --shell /bin/bash
  rdev-client -s wss://rdev.example.com -i rpi4 --shell /usr/bin/fish

Environment variables:
  RDEV_SHELL    Shell to use (overrides --shell flag)
  RDEV_SERVER   Server URL (overrides --server flag)
  RDEV_ID       Client ID (overrides --id flag)
`)
			os.Exit(0)
		}
	}

	// Environment variable overrides
	if serverURL == "" {
		serverURL = os.Getenv("RDEV_SERVER")
	}
	if clientID == "" {
		clientID = os.Getenv("RDEV_ID")
	}
	if shell == "" {
		shell = os.Getenv("RDEV_SHELL")
	}
	if serverURL == "" {
		fmt.Println("Error: --server is required")
		fmt.Println("Usage: rdev-client -s <server-url> [options]")
		fmt.Println("Example: rdev-client -s ws://1.2.3.4:8080 -i mydevice")
		os.Exit(1)
	}

	if !strings.HasPrefix(serverURL, "ws://") && !strings.HasPrefix(serverURL, "wss://") {
		serverURL = "ws://" + serverURL
	}

	if clientID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			clientID = "unknown"
		} else {
			clientID = hostname
		}
	}

	fmt.Println()
	fmt.Println("  ╔═══════════════════════════════════════════╗")
	fmt.Println("  ║         RDev Remote Debug Client          ║")
	fmt.Println("  ╠═══════════════════════════════════════════╣")
	fmt.Printf("  ║  Server:  %-31s  ║\n", serverURL)
	fmt.Printf("  ║  ID:      %-31s  ║\n", clientID)
	if shell != "" {
		fmt.Printf("  ║  Shell:   %-31s  ║\n", shell)
	}
	if password != "" {
		fmt.Println("  ║  Auth:    password                        ║")
	}
	fmt.Println("  ╚═══════════════════════════════════════════╝")
	fmt.Println()

	c := client.NewClient(serverURL, clientID, password, shell)
	if err := c.Run(); err != nil {
		log.Fatalf("client error: %v", err)
	}
}
