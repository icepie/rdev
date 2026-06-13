module rdev

go 1.25.0

require (
	github.com/aymanbagabas/go-pty v0.2.3
	github.com/gliderlabs/ssh v0.3.7
	github.com/lxzan/gws v1.9.1
	github.com/pkg/sftp v1.13.9
	golang.org/x/crypto v0.51.0
)

require (
	github.com/anmitsu/go-shlex v0.0.0-20200514113438-38f4b401e2be // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/u-root/u-root v0.16.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
)

replace github.com/gliderlabs/ssh => ./internal/sshlib
