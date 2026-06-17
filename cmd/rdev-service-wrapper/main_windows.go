//go:build windows

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

type wrapperConfig struct {
	WorkDir     string   `json:"workDir"`
	Log         string   `json:"log"`
	Interactive bool     `json:"interactive"`
	Command     []string `json:"command"`
}

type wrapperService struct {
	name string
	cfg  wrapperConfig

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan error
}

func main() {
	var serviceName string
	var configPath string
	var workDir string
	var logPath string

	flag.StringVar(&serviceName, "name", "", "Windows service name")
	flag.StringVar(&configPath, "config", "", "JSON config path")
	flag.StringVar(&workDir, "workdir", "", "child working directory")
	flag.StringVar(&logPath, "log", "", "log path")
	flag.Parse()

	if serviceName == "" {
		serviceName = strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0]))
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		fatal(err)
	}
	if workDir != "" {
		cfg.WorkDir = workDir
	}
	if logPath != "" {
		cfg.Log = logPath
	}
	if args := flag.Args(); len(args) > 0 {
		cfg.Command = args
	}
	if len(cfg.Command) == 0 {
		fatal(fmt.Errorf("missing child command; use --config or pass command after flags"))
	}

	logFile, err := openLog(cfg.Log)
	if err != nil {
		fatal(err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	isService, err := svc.IsWindowsService()
	if err != nil {
		fatal(err)
	}

	service := &wrapperService{name: serviceName, cfg: cfg}
	if isService {
		if err := svc.Run(serviceName, service); err != nil {
			log.Printf("service failed: %v", err)
			os.Exit(1)
		}
		return
	}

	if err := service.runConsole(); err != nil {
		log.Printf("console failed: %v", err)
		os.Exit(1)
	}
}

func loadConfig(path string) (wrapperConfig, error) {
	if path == "" {
		return wrapperConfig{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return wrapperConfig{}, err
	}
	var cfg wrapperConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return wrapperConfig{}, err
	}
	return cfg, nil
}

func openLog(path string) (*os.File, error) {
	if path == "" {
		path = filepath.Join(os.TempDir(), "rdev-service-wrapper.log")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

func (s *wrapperService) Execute(args []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	if err := s.start(); err != nil {
		log.Printf("start child failed: %v", err)
		changes <- svc.Status{State: svc.Stopped}
		return false, 1
	}
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				s.stop()
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			default:
				log.Printf("unsupported service request: %v", request.Cmd)
			}
		case err := <-s.done:
			if err != nil {
				log.Printf("child exited: %v", err)
			} else {
				log.Printf("child exited")
			}
			changes <- svc.Status{State: svc.Stopped}
			return false, 1
		}
	}
}

func (s *wrapperService) runConsole() error {
	if err := s.start(); err != nil {
		return err
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	select {
	case <-interrupt:
		s.stop()
		return nil
	case err := <-s.done:
		return err
	}
}

func (s *wrapperService) start() error {
	command := s.cfg.Command
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = s.cfg.WorkDir

	logWriter := log.Writer()
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter

	var token windows.Token
	if s.cfg.Interactive {
		var err error
		token, err = activeConsoleUserToken()
		if err != nil {
			return fmt.Errorf("get active console user token: %w", err)
		}
		defer token.Close()
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Token:         syscall.Token(token),
			CreationFlags: windows.CREATE_NEW_CONSOLE,
		}
		if env, err := token.Environ(true); err == nil {
			cmd.Env = env
		} else {
			log.Printf("load interactive user environment failed: %v", err)
		}
		log.Printf("starting interactive child in active console session: %q", command)
	} else {
		log.Printf("starting child: %q", command)
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan error, 1)
	s.mu.Lock()
	s.cmd = cmd
	s.done = done
	s.mu.Unlock()
	go func() { done <- cmd.Wait() }()
	return nil
}

func activeConsoleUserToken() (windows.Token, error) {
	sessionID := windows.WTSGetActiveConsoleSessionId()
	if sessionID == 0xffffffff {
		return 0, fmt.Errorf("no active console session")
	}

	var impersonation windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &impersonation); err != nil {
		return 0, err
	}
	defer impersonation.Close()

	var primary windows.Token
	if err := windows.DuplicateTokenEx(
		impersonation,
		windows.MAXIMUM_ALLOWED,
		nil,
		windows.SecurityImpersonation,
		windows.TokenPrimary,
		&primary,
	); err != nil {
		return 0, err
	}
	return primary, nil
}

func (s *wrapperService) stop() {
	s.mu.Lock()
	cmd := s.cmd
	done := s.done
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}

	log.Printf("stopping child pid=%d", cmd.Process.Pid)
	_ = cmd.Process.Kill()
	select {
	case <-done:
		log.Printf("child stopped")
	case <-time.After(10 * time.Second):
		log.Printf("child did not stop before timeout")
	}
}
