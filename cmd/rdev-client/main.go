package main

import (
	"fmt"
	"log"
	"net"
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
		sshPort   string
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
		case "--ssh-port":
			if i+1 < len(os.Args) {
				sshPort = os.Args[i+1]
				i++
			}
		case "--help":
			fmt.Print(`Usage: rdev-client -s <server-url> [options]

Options:
  --server, -s    Server URL (required, e.g. ws://1.2.3.4:8080)
  --id, -i        Client/Device ID (default: hostname)
  --password, -p  Password for SSH auth (optional, enables password login)
  --shell, -S     Shell to use (default: $SHELL or /bin/sh or cmd.exe)
  --ssh-port      Server SSH port (for connection hint display, default: 2222)

Examples:
  rdev-client -s ws://1.2.3.4:8080 -i mydevice -p secret123
  rdev-client -s ws://1.2.3.4:8080 -i mydevice --shell /bin/bash --ssh-port 2200

Environment variables:
  RDEV_SHELL     Shell to use (overrides --shell flag)
  RDEV_SERVER    Server URL (overrides --server flag)
  RDEV_ID        Client ID (overrides --id flag)
  RDEV_SSH_PORT  Server SSH port (overrides --ssh-port flag)
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
	if sshPort == "" {
		sshPort = os.Getenv("RDEV_SSH_PORT")
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

	if sshPort == "" {
		sshPort = "2222"
	}

	serverHost := parseWSHost(serverURL)

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

	// Print connection hints after successful connect
	connectPrinted := false
	c.OnConnect = func(cli *client.Client) {
		if connectPrinted {
			return // only print once (suppress on reconnect)
		}
		connectPrinted = true

		fmt.Println("  ── How to Connect ─────────────────────────────")
		fmt.Printf("  SSH:      ssh %s@%s -p %s\n", clientID, serverHost, sshPort)
		if password != "" {
			fmt.Printf("            sshpass -p *** ssh %s@%s -p %s\n", clientID, serverHost, sshPort)
		}
		fmt.Printf("  SFTP:     sftp -P %s %s@%s\n", sshPort, clientID, serverHost)
		fmt.Printf("  SCP:      scp -P %s file %s@%s:~/\n", sshPort, clientID, serverHost)
		fmt.Printf("  Dashboard: http://%s\n", serverHost)
		fmt.Println("  ────────────────────────────────────────────────")
		fmt.Println()
	}

	if err := c.Run(); err != nil {
		log.Fatalf("client error: %v", err)
	}
}

// parseWSHost extracts the host portion from a ws:// or wss:// URL.
func parseWSHost(wsURL string) string {
	u := wsURL
	u = strings.TrimPrefix(u, "ws://")
	u = strings.TrimPrefix(u, "wss://")
	if idx := strings.Index(u, "/"); idx >= 0 {
		u = u[:idx]
	}
	host, _, err := net.SplitHostPort(u)
	if err != nil {
		return u
	}
	return host
}
