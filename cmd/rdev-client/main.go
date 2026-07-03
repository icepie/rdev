package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"rdev/internal/client"
	"rdev/internal/updater"
)

var version = "dev"

func main() {
	var (
		serverURL      string
		clientID       string
		password       string
		shell          string
		autoUpdate     = true
		updateInterval = time.Minute
		reconnectMin   = time.Second
		reconnectMax   = 30 * time.Second
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
		case "--no-auto-update":
			autoUpdate = false
		case "--auto-update":
			if i+1 < len(os.Args) {
				autoUpdate = parseBoolDefault(os.Args[i+1], true)
				i++
			}
		case "--update-interval":
			if i+1 < len(os.Args) {
				if d, err := time.ParseDuration(os.Args[i+1]); err == nil && d > 0 {
					updateInterval = d
				}
				i++
			}
		case "--reconnect-min":
			if i+1 < len(os.Args) {
				if d, err := time.ParseDuration(os.Args[i+1]); err == nil && d > 0 {
					reconnectMin = d
				}
				i++
			}
		case "--reconnect-max":
			if i+1 < len(os.Args) {
				if d, err := time.ParseDuration(os.Args[i+1]); err == nil && d > 0 {
					reconnectMax = d
				}
				i++
			}
		case "--version", "-v":
			fmt.Println(version)
			os.Exit(0)
		case "--help":
			fmt.Print(`Usage: rdev-client -s <server-url> [options]

Options:
  --server, -s    Server URL(s), comma-separated fallback list (e.g. tcp://1.2.3.4:8081,kcp://1.2.3.4:8082,ws://1.2.3.4:8080)
  --id, -i        Client/Device ID (default: hostname)
  --password, -p  Password for SSH auth (optional, enables password login)
  --shell, -S     Shell to use (default: $SHELL or /bin/sh or cmd.exe)
  --no-auto-update Disable built-in GitHub release auto-update
  --auto-update    Enable/disable auto-update explicitly (true/false)
  --update-interval Auto-update polling interval (default 1m)
  --reconnect-min Minimum reconnect delay (default 1s)
  --reconnect-max Maximum reconnect delay (default 30s)
  --version, -v   Print version and exit

SSH port is auto-detected from server — no need to specify manually.

Examples:
  rdev-client -s tcp://1.2.3.4:8081,kcp://1.2.3.4:8082,ws://1.2.3.4:8080 -i mydevice -p secret123
  rdev-client -s wss://rdev.example.com -i rpi4 --shell /usr/bin/fish

Environment variables:
  RDEV_SHELL    Shell to use (overrides --shell flag)
  RDEV_SERVER   Server URL (overrides --server flag)
  RDEV_ID       Client ID (overrides --id flag)
  RDEV_AUTO_UPDATE true/false (default true)
  RDEV_UPDATE_INTERVAL duration like 5m or 1h
  RDEV_RECONNECT_MIN minimum reconnect delay (default 1s)
  RDEV_RECONNECT_MAX maximum reconnect delay (default 30s)
  RDEV_UPDATE_PROXY comma-separated GitHub proxy prefixes
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
	if env := os.Getenv("RDEV_AUTO_UPDATE"); env != "" {
		autoUpdate = parseBoolDefault(env, autoUpdate)
	}
	if env := os.Getenv("RDEV_UPDATE_INTERVAL"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			updateInterval = d
		}
	}
	if env := os.Getenv("RDEV_RECONNECT_MIN"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			reconnectMin = d
		}
	}
	if env := os.Getenv("RDEV_RECONNECT_MAX"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			reconnectMax = d
		}
	}
	if serverURL == "" {
		fmt.Println("Error: --server is required")
		fmt.Println("Usage: rdev-client -s <server-url> [options]")
		fmt.Println("Example: rdev-client -s tcp://1.2.3.4:8081,kcp://1.2.3.4:8082,ws://1.2.3.4:8080 -i mydevice")
		os.Exit(1)
	}

	serverURL = normalizeServerListForDisplay(serverURL)

	if clientID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			clientID = "unknown"
		} else {
			clientID = hostname
		}
	}

	serverHost := parseServerHost(serverURL)

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
	c.SetVersion("go/" + version)
	c.SetReconnectDelays(reconnectMin, reconnectMax)

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

	updater.Start(context.Background(), updater.Config{App: "client", Version: version, Enabled: autoUpdate, Interval: updateInterval})

	if err := c.Run(); err != nil {
		log.Fatalf("client error: %v", err)
	}
}

func parseBoolDefault(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on", "enable", "enabled":
		return true
	case "0", "false", "no", "n", "off", "disable", "disabled":
		return false
	default:
		return fallback
	}
}

func normalizeServerListForDisplay(value string) string {
	parts := strings.Split(value, ",")
	for i, p := range parts {
		u := strings.TrimSpace(p)
		for _, scheme := range []string{"tcp", "kcp", "udp", "ws", "wss", "http", "https"} {
			prefix := scheme + "://"
			if strings.HasPrefix(u, scheme+":///") {
				u = prefix + strings.TrimLeft(u[len(prefix):], "/")
			}
		}
		if !strings.Contains(u, "://") {
			u = "tcp://" + u
		}
		parts[i] = u
	}
	return strings.Join(parts, ",")
}

// parseServerHost extracts the host portion from the first endpoint.
func parseServerHost(serverURL string) string {
	u := strings.TrimSpace(strings.Split(serverURL, ",")[0])
	for _, scheme := range []string{"tcp://", "kcp://", "udp://", "ws://", "wss://", "http://", "https://"} {
		u = strings.TrimPrefix(u, scheme)
	}
	if idx := strings.Index(u, "/"); idx >= 0 {
		u = u[:idx]
	}
	host, _, err := net.SplitHostPort(u)
	if err != nil {
		return u
	}
	return host
}
