package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rdev/internal/server"
	"rdev/internal/updater"
)

var version = "dev"

func main() {
	var (
		httpAddr             = ":8080"
		sshAddr              = ":2222"
		dataDir              = ""
		adminToken           = ""
		maxSessions          = 0
		maxForwards          = 0
		batchConcurrency     = 0
		autoUpdate           = true
		updateInterval       = time.Minute
		aliyunPanConfig      = ""
		aliyunPanDeviceID    = ""
		aliyunPanRoot        = ""
		aliyunPanTmpDir      = ""
		aliyunPanTransferDir = ""
		aliyunPanPassword    = ""
	)

	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--http", "-h":
			if i+1 < len(os.Args) {
				httpAddr = os.Args[i+1]
				i++
			}
		case "--ssh", "-s":
			if i+1 < len(os.Args) {
				sshAddr = os.Args[i+1]
				i++
			}
		case "--data", "-d":
			if i+1 < len(os.Args) {
				dataDir = os.Args[i+1]
				i++
			}
		case "--admin-token", "-t":
			if i+1 < len(os.Args) {
				adminToken = os.Args[i+1]
				i++
			}
		case "--max-sessions":
			if i+1 < len(os.Args) {
				fmt.Sscanf(os.Args[i+1], "%d", &maxSessions)
				i++
			}
		case "--max-forwards":
			if i+1 < len(os.Args) {
				fmt.Sscanf(os.Args[i+1], "%d", &maxForwards)
				i++
			}
		case "--batch-concurrency":
			if i+1 < len(os.Args) {
				fmt.Sscanf(os.Args[i+1], "%d", &batchConcurrency)
				i++
			}
		case "--aliyunpan-config":
			if i+1 < len(os.Args) {
				aliyunPanConfig = os.Args[i+1]
				i++
			}
		case "--aliyunpan-id":
			if i+1 < len(os.Args) {
				aliyunPanDeviceID = os.Args[i+1]
				i++
			}
		case "--aliyunpan-root":
			if i+1 < len(os.Args) {
				aliyunPanRoot = os.Args[i+1]
				i++
			}
		case "--aliyunpan-tmp-dir":
			if i+1 < len(os.Args) {
				aliyunPanTmpDir = os.Args[i+1]
				i++
			}
		case "--aliyunpan-transfer-dir":
			if i+1 < len(os.Args) {
				aliyunPanTransferDir = os.Args[i+1]
				i++
			}
		case "--aliyunpan-password":
			if i+1 < len(os.Args) {
				aliyunPanPassword = os.Args[i+1]
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
		case "--version", "-v":
			fmt.Println(version)
			os.Exit(0)
		case "--help":
			fmt.Print(`Usage: rdev-server [options]

Options:
  --http, -h  HTTP/WS listen address (default :8080)
  --ssh, -s   SSH listen address (default :2222)
  --data, -d  Data directory for host key & authorized_keys (default ~/.rdev)
  --admin-token, -t  Token for Web UI APIs, terminal, batch, upload (optional)
  --max-sessions     Max concurrent sessions per device (default 256)
  --max-forwards     Max concurrent TCP forwards per device (default 1024)
  --batch-concurrency Max concurrent batch operations (default GOMAXPROCS*8)
  --no-auto-update Disable built-in GitHub release auto-update
  --auto-update    Enable/disable auto-update explicitly (true/false)
  --update-interval Auto-update polling interval (default 1m)
  --aliyunpan-config Enable AliyunPan virtual file backend with aliyunpan_config.json
  --aliyunpan-id     Virtual device ID for AliyunPan backend (default aliyunpan)
  --aliyunpan-root   Root path exposed from AliyunPan (default /)
  --aliyunpan-tmp-dir Temp directory for AliyunPan uploads/SFTP staging
  --aliyunpan-transfer-dir AliyunPan cloud directory for device relay transfers (default /rdev-transfer)
  --aliyunpan-password Optional SSH password for AliyunPan backend; authorized_keys also work
  --version, -v    Print version and exit

Features:
  - SSH shell/exec/sftp/scp access to connected devices
  - Public key (authorized_keys) and password authentication
  - Local port forwarding (-L) and remote port forwarding (-R)
  - Web terminal, batch commands, file distribution

Examples:
  rdev-server
  rdev-server --data /etc/rdev
`)
			os.Exit(0)
		}
	}

	if adminToken == "" {
		adminToken = os.Getenv("RDEV_ADMIN_TOKEN")
	}
	if env := os.Getenv("RDEV_AUTO_UPDATE"); env != "" {
		autoUpdate = parseBoolDefault(env, autoUpdate)
	}
	if env := os.Getenv("RDEV_UPDATE_INTERVAL"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			updateInterval = d
		}
	}

	if env := os.Getenv("RDEV_ALIYUNPAN_CONFIG"); env != "" && aliyunPanConfig == "" {
		aliyunPanConfig = env
	}
	if env := os.Getenv("RDEV_ALIYUNPAN_ID"); env != "" && aliyunPanDeviceID == "" {
		aliyunPanDeviceID = env
	}
	if env := os.Getenv("RDEV_ALIYUNPAN_ROOT"); env != "" && aliyunPanRoot == "" {
		aliyunPanRoot = env
	}
	if env := os.Getenv("RDEV_ALIYUNPAN_TMP_DIR"); env != "" && aliyunPanTmpDir == "" {
		aliyunPanTmpDir = env
	}
	if env := os.Getenv("RDEV_ALIYUNPAN_TRANSFER_DIR"); env != "" && aliyunPanTransferDir == "" {
		aliyunPanTransferDir = env
	}
	if env := os.Getenv("RDEV_ALIYUNPAN_PASSWORD"); env != "" && aliyunPanPassword == "" {
		aliyunPanPassword = env
	}

	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".rdev")
	}
	os.MkdirAll(dataDir, 0700)

	hostKeyPath := filepath.Join(dataDir, "host_key")
	authorizedKeysPath := filepath.Join(dataDir, "authorized_keys")

	if _, err := os.Stat(authorizedKeysPath); os.IsNotExist(err) {
		os.WriteFile(authorizedKeysPath, []byte(
			"# RDev authorized_keys\n"+
				"# Add your SSH public keys here for passwordless access\n"+
				"# e.g.: ssh-ed25519 AAAA... user@host\n"), 0600)
		log.Printf("created %s — add your SSH public keys there for passwordless access", authorizedKeysPath)
	}

	// Detect outbound IP early for connection hints
	outboundIP := detectOutboundIP()
	httpPort := portFromAddr(httpAddr)
	sshPort := portFromAddr(sshAddr)

	srv := server.NewServer()
	srv.SSHPort = sshPort
	srv.HTTPHost = outboundIP + ":" + httpPort
	srv.AdminToken = adminToken
	if maxSessions > 0 {
		srv.MaxSessions = maxSessions
	}
	if maxForwards > 0 {
		srv.MaxForwards = maxForwards
	}
	if batchConcurrency > 0 {
		srv.BatchConcurrency = batchConcurrency
	}

	aliyunPanOptions := server.AliyunPanConfig{Enabled: aliyunPanConfig != "", ConfigPath: aliyunPanConfig, DeviceID: aliyunPanDeviceID, Root: aliyunPanRoot, TempDir: aliyunPanTmpDir, TransferDir: aliyunPanTransferDir, Password: aliyunPanPassword}
	aliyunPanOptionsPath := filepath.Join(dataDir, "aliyunpan.json")
	if fileOptions, err := server.LoadAliyunPanConfig(aliyunPanOptionsPath); err == nil {
		if fileOptions.Enabled || aliyunPanOptions.ConfigPath == "" {
			aliyunPanOptions = fileOptions
		}
		if aliyunPanConfig != "" {
			aliyunPanOptions.Enabled = true
			aliyunPanOptions.ConfigPath = aliyunPanConfig
		}
		if aliyunPanDeviceID != "" {
			aliyunPanOptions.DeviceID = aliyunPanDeviceID
		}
		if aliyunPanRoot != "" {
			aliyunPanOptions.Root = aliyunPanRoot
		}
		if aliyunPanTmpDir != "" {
			aliyunPanOptions.TempDir = aliyunPanTmpDir
		}
		if aliyunPanTransferDir != "" {
			aliyunPanOptions.TransferDir = aliyunPanTransferDir
		}
		if aliyunPanPassword != "" {
			aliyunPanOptions.Password = aliyunPanPassword
		}
	}
	if aliyunPanOptions.ConfigPath != "" && !filepath.IsAbs(aliyunPanOptions.ConfigPath) {
		aliyunPanOptions.ConfigPath = filepath.Join(dataDir, aliyunPanOptions.ConfigPath)
	}
	if aliyunPanOptions.Enabled && aliyunPanOptions.ConfigPath != "" {
		backend, err := server.NewAliyunPanBackend(aliyunPanOptions.ConfigPath, aliyunPanOptions)
		if err != nil {
			log.Printf("aliyunpan backend disabled: %v", err)
		} else {
			srv.FileBackend = backend
			log.Printf("aliyunpan backend enabled as device %q", backend.ID())
		}
	}

	sshServer, err := server.NewSSHServer(srv, sshAddr, hostKeyPath, authorizedKeysPath)
	if err != nil {
		log.Fatalf("SSH server init error: %v", err)
	}
	sshServer.WatchAuthorizedKeys(authorizedKeysPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", srv.HandleWS)
	mux.HandleFunc("/terminal", srv.HandleTerminalWS)
	mux.HandleFunc("/desktop", srv.HandleDesktopWS)
	mux.HandleFunc("/session", srv.HandleSessionAttachWS)
	mux.HandleFunc("/batch", srv.HandleBatchWS)
	mux.HandleFunc("/files", srv.HandleFilesWS)
	mux.HandleFunc("/api/clients", srv.HandleAPI)
	mux.HandleFunc("/api/sessions", srv.HandleSessionsAPI)
	mux.HandleFunc("/api/devices", srv.HandleTerminalAPI)
	mux.HandleFunc("/api/batch/devices", srv.HandleBatchDevicesAPI)
	mux.HandleFunc("/api/config", srv.HandleConfigAPI)
	mux.HandleFunc("/api/upload", srv.HandleFileUpload)
	mux.Handle("/", srv.StaticHandler())

	go func() {
		if err := sshServer.ListenAndServe(); err != nil {
			log.Fatalf("SSH server error: %v", err)
		}
	}()

	fmt.Println()
	fmt.Println("  ╔════════════════════════════════════════════════╗")
	fmt.Println("  ║          RDev Remote Debug Server              ║")
	fmt.Println("  ╠════════════════════════════════════════════════╣")
	fmt.Printf("  ║  Web:    http://%s:%s                  ║\n", outboundIP, httpPort)
	fmt.Printf("  ║  SSH:    %s:%s                       ║\n", outboundIP, sshPort)
	fmt.Printf("  ║  Data:   %-37s║\n", dataDir)
	if adminToken != "" {
		fmt.Println("  ║  WebAuth: enabled                              ║")
	}
	fmt.Println("  ╚════════════════════════════════════════════════╝")

	// Start HTTP listener before printing info, so we know it's bound
	httpListener, err := net.Listen("tcp", httpAddr)
	if err != nil {
		log.Fatalf("HTTP listen error: %v", err)
	}

	// Print connection examples — server is ready now
	fmt.Println()
	fmt.Println("  ── Connection ──────────────────────────────────")
	fmt.Printf("  Client:   ./rdev-client -s ws://%s:%s -i <device-id>\n", outboundIP, httpPort)
	fmt.Printf("  SSH:      ssh <device-id>@%s -p %s\n", outboundIP, sshPort)
	fmt.Printf("  Dashboard: http://%s:%s\n", outboundIP, httpPort)
	fmt.Println("  ────────────────────────────────────────────────")
	fmt.Println()
	fmt.Printf("  Host key:     %s\n", hostKeyPath)
	fmt.Printf("  Auth keys:    %s\n", authorizedKeysPath)
	fmt.Println()

	updater.Start(context.Background(), updater.Config{App: "server", Version: version, Enabled: autoUpdate, Interval: updateInterval})

	log.Printf("HTTP server listening on %s", httpAddr)
	if err := http.Serve(httpListener, mux); err != nil {
		log.Fatalf("HTTP server error: %v", err)
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

// detectOutboundIP returns the preferred outbound IP (local network).
func detectOutboundIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:53", 1*time.Second)
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			return addr.IP.String()
		}
	}
	return "0.0.0.0"
}

// portFromAddr extracts port from ":8080" or "0.0.0.0:8080".
func portFromAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return addr[1:]
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return port
}
