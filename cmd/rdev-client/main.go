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
  --password, -p  Password for SSH auth (optional, enables password login)
  --shell, -S     Shell to use (default: $SHELL or /bin/sh or cmd.exe)

SSH port is auto-detected from server — no need to specify manually.

Examples:
  rdev-client -s ws://1.2.3.4:8080 -i mydevice -p secret123
  rdev-client -s wss://rdev.example.com -i rpi4 --shell /usr/bin/fish

Environment variables:
  RDEV_SHELL    Shell to use (overrides --shell flag)
  RDEV_SERVER   Server URL (overrides --server flag)
  RDEV_ID       Client ID (overrides --id flag)
`)
			os.Exit(0)
		}
	}

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

	// Normalize: ensure exactly "ws://" or "wss://" (fix triple-slash from shell quoting)
	if strings.HasPrefix(serverURL, "wss:///") {
		serverURL = "wss://" + strings.TrimLeft(serverURL[len("wss://"):], "/")
	} else if strings.HasPrefix(serverURL, "ws:///") {
		serverURL = "ws://" + strings.TrimLeft(serverURL[len("ws://"):], "/")
	} else if !strings.HasPrefix(serverURL, "ws://") && !strings.HasPrefix(serverURL, "wss://") {
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
	authMode := "open (no password)"
	if password != "" {
		authMode = "password"
	}
	fmt.Printf("  ║  Auth:    %-31s  ║\n", authMode)
	if password != "" {
		fmt.Printf("  ║  Pass:    %-31s  ║\n", password)
	}
	fmt.Println("  ╚═══════════════════════════════════════════╝")
	fmt.Println()

	c := client.NewClient(serverURL, clientID, password, shell)

	// Print connection hints after successful connect
	connectPrinted := false
	c.OnConnect = func(cli *client.Client) {
		if connectPrinted {
			return
		}
		connectPrinted = true

		sshPort := cli.SSHPort()
		if sshPort == "" {
			sshPort = "2222" // fallback
		}
		assignedID := cli.ClientID()
		if assignedID == "" {
			assignedID = clientID
		}

		fmt.Println("  ── How to Connect ─────────────────────────────")
		fmt.Printf("  SSH:      ssh %s@%s -p %s\n", assignedID, serverHost, sshPort)
		if password != "" {
			fmt.Printf("  Password: %s\n", password)
			fmt.Printf("            sshpass -p '%s' ssh %s@%s -p %s\n", password, assignedID, serverHost, sshPort)
		} else {
			fmt.Println("  Password: <none> (open mode)")
		}
		fmt.Printf("  SFTP:     sftp -P %s %s@%s\n", sshPort, assignedID, serverHost)
		fmt.Printf("  SCP:      scp -P %s file %s@%s:~/\n", sshPort, assignedID, serverHost)
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
