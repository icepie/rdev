module rdev

go 1.25.0

require (
	fyne.io/systray v1.12.2
	github.com/aymanbagabas/go-pty v0.2.3
	github.com/gliderlabs/ssh v0.3.8
	github.com/lxzan/gws v1.9.1
	github.com/pkg/sftp v1.13.10
	golang.org/x/crypto v0.53.0
)

require (
	github.com/anmitsu/go-shlex v0.0.0-20200514113438-38f4b401e2be // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/u-root/u-root v0.16.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/gliderlabs/ssh => ./internal/sshlib
