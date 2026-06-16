//go:build linux

package client

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/BurntSushi/xgb"
)

type linuxX11Env struct {
	Display               string
	XAuthority            string
	Home                  string
	XDGRuntimeDir         string
	DBusSessionBusAddress string
	Source                string
}

var linuxX11EnvMu sync.Mutex

func linuxX11ConnectAny() (*xgb.Conn, linuxX11Env, error) {
	candidates := discoverLinuxX11Envs()
	if len(candidates) == 0 {
		return nil, linuxX11Env{}, fmt.Errorf("no X11 display candidates found")
	}
	var errs []string
	for _, env := range candidates {
		var conn *xgb.Conn
		err := withLinuxX11Env(env, func() error {
			var err error
			conn, err = xgb.NewConnDisplay(env.Display)
			return err
		})
		if err == nil {
			return conn, env, nil
		}
		errs = append(errs, fmt.Sprintf("%s (%s): %v", env.Display, env.Source, err))
	}
	return nil, linuxX11Env{}, fmt.Errorf("connect X11: %s", strings.Join(errs, "; "))
}

func linuxX11Available() bool {
	conn, _, err := linuxX11ConnectAny()
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func discoverLinuxX11Envs() []linuxX11Env {
	var out []linuxX11Env
	seen := map[string]bool{}
	add := func(env linuxX11Env) {
		env.Display = strings.TrimSpace(env.Display)
		if env.Display == "" {
			return
		}
		if env.Home == "" {
			env.Home = os.Getenv("HOME")
		}
		key := env.Display + "\x00" + env.XAuthority + "\x00" + env.Home + "\x00" + env.XDGRuntimeDir
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, env)
	}

	add(linuxX11Env{
		Display:               os.Getenv("DISPLAY"),
		XAuthority:            os.Getenv("XAUTHORITY"),
		Home:                  os.Getenv("HOME"),
		XDGRuntimeDir:         os.Getenv("XDG_RUNTIME_DIR"),
		DBusSessionBusAddress: os.Getenv("DBUS_SESSION_BUS_ADDRESS"),
		Source:                "env",
	})

	uidHomes := map[int]string{}
	for _, env := range linuxX11EnvsFromProc(uidHomes) {
		add(env)
	}

	for _, env := range linuxX11EnvsFromSockets(uidHomes) {
		add(env)
	}

	return out
}

func linuxX11EnvsFromProc(uidHomes map[int]string) []linuxX11Env {
	paths, _ := filepath.Glob("/proc/[0-9]*/environ")
	var envs []linuxX11Env
	for _, path := range paths {
		pidDir := filepath.Dir(path)
		uid := linuxPathUID(pidDir)
		if uid >= 0 {
			if home := linuxHomeForUID(uid); home != "" {
				uidHomes[uid] = home
			}
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		m := parseProcEnv(data)
		display := m["DISPLAY"]
		if display == "" {
			continue
		}
		home := m["HOME"]
		if home == "" && uid >= 0 {
			home = uidHomes[uid]
		}
		envs = append(envs, linuxX11Env{
			Display:               display,
			XAuthority:            firstReadable(m["XAUTHORITY"], linuxXAuthCandidates(uid, home, m["XDG_RUNTIME_DIR"])...),
			Home:                  home,
			XDGRuntimeDir:         m["XDG_RUNTIME_DIR"],
			DBusSessionBusAddress: m["DBUS_SESSION_BUS_ADDRESS"],
			Source:                "proc:" + filepath.Base(pidDir),
		})
	}
	return envs
}

func linuxX11EnvsFromSockets(uidHomes map[int]string) []linuxX11Env {
	sockets, _ := filepath.Glob("/tmp/.X11-unix/X*")
	if len(sockets) == 0 {
		return nil
	}
	uids := linuxLikelyDesktopUIDs(uidHomes)
	var envs []linuxX11Env
	for _, socket := range sockets {
		name := filepath.Base(socket)
		if !strings.HasPrefix(name, "X") || len(name) == 1 {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimPrefix(name, "X")); err != nil {
			continue
		}
		display := ":" + strings.TrimPrefix(name, "X")
		for _, uid := range uids {
			home := uidHomes[uid]
			if home == "" {
				home = linuxHomeForUID(uid)
			}
			runtimeDir := "/run/user/" + strconv.Itoa(uid)
			envs = append(envs, linuxX11Env{
				Display:               display,
				XAuthority:            firstReadable("", linuxXAuthCandidates(uid, home, runtimeDir)...),
				Home:                  home,
				XDGRuntimeDir:         runtimeDir,
				DBusSessionBusAddress: linuxDBusAddress(runtimeDir),
				Source:                "socket:" + socket,
			})
		}
	}
	return envs
}

func withLinuxX11Env(env linuxX11Env, fn func() error) error {
	linuxX11EnvMu.Lock()
	defer linuxX11EnvMu.Unlock()

	keys := []string{"DISPLAY", "XAUTHORITY", "HOME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}
	old := make(map[string]string, len(keys))
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		old[key], set[key] = os.LookupEnv(key)
	}
	defer func() {
		for _, key := range keys {
			if set[key] {
				os.Setenv(key, old[key])
			} else {
				os.Unsetenv(key)
			}
		}
	}()

	setOrUnset("DISPLAY", env.Display)
	setOrUnset("XAUTHORITY", env.XAuthority)
	setOrUnset("HOME", env.Home)
	setOrUnset("XDG_RUNTIME_DIR", env.XDGRuntimeDir)
	setOrUnset("DBUS_SESSION_BUS_ADDRESS", env.DBusSessionBusAddress)
	return fn()
}

func setOrUnset(key, value string) {
	if value == "" {
		os.Unsetenv(key)
		return
	}
	os.Setenv(key, value)
}

func parseProcEnv(data []byte) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(string(data), "\x00") {
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if ok {
			m[key] = value
		}
	}
	return m
}

func linuxLikelyDesktopUIDs(uidHomes map[int]string) []int {
	seen := map[int]bool{}
	var uids []int
	add := func(uid int) {
		if uid < 0 || seen[uid] {
			return
		}
		seen[uid] = true
		uids = append(uids, uid)
	}
	add(os.Getuid())
	for uid := range uidHomes {
		add(uid)
	}
	entries, _ := os.ReadDir("/run/user")
	for _, entry := range entries {
		uid, err := strconv.Atoi(entry.Name())
		if err == nil {
			add(uid)
		}
	}
	sort.Ints(uids)
	return uids
}

func linuxXAuthCandidates(uid int, home, runtimeDir string) []string {
	var paths []string
	if home != "" {
		paths = append(paths, filepath.Join(home, ".Xauthority"))
	}
	if runtimeDir == "" && uid >= 0 {
		runtimeDir = "/run/user/" + strconv.Itoa(uid)
	}
	if runtimeDir != "" {
		paths = append(paths,
			filepath.Join(runtimeDir, "gdm", "Xauthority"),
			filepath.Join(runtimeDir, "Xauthority"),
			filepath.Join(runtimeDir, ".Xauthority"),
		)
	}
	if uid >= 0 {
		for _, glob := range []string{
			"/tmp/xauth_*",
			"/tmp/.Xauthority-*",
			"/var/run/gdm3/auth-for-*/database",
			"/run/gdm3/auth-for-*/database",
		} {
			matches, _ := filepath.Glob(glob)
			for _, match := range matches {
				if linuxPathUID(match) == uid {
					paths = append(paths, match)
				}
			}
		}
	}
	return paths
}

func firstReadable(primary string, candidates ...string) string {
	paths := append([]string{}, primary)
	paths = append(paths, candidates...)
	for _, path := range paths {
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if err == nil {
			file.Close()
			return path
		}
	}
	return primary
}

func linuxDBusAddress(runtimeDir string) string {
	if runtimeDir == "" {
		return ""
	}
	bus := filepath.Join(runtimeDir, "bus")
	if _, err := os.Stat(bus); err != nil {
		return ""
	}
	return "unix:path=" + bus
}

func linuxPathUID(path string) int {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(st.Uid)
}

func linuxHomeForUID(uid int) string {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	needle := ":" + strconv.Itoa(uid) + ":"
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, needle) {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) >= 6 && fields[2] == strconv.Itoa(uid) {
			return fields[5]
		}
	}
	return ""
}
