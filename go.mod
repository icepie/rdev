module rdev

go 1.26.0

require (
	github.com/BurntSushi/xgb v0.0.0-20210121224620-deaf085860bc
	github.com/anmitsu/go-shlex v0.0.0-20200514113438-38f4b401e2be
	github.com/aymanbagabas/go-pty v0.2.3
	github.com/ebitengine/purego v0.11.0
	github.com/gliderlabs/ssh v0.3.8
	github.com/go-git/go-git/v5 v5.19.2
	github.com/iamacarpet/go-winpty v1.0.4
	github.com/lxzan/gws v1.10.1
	github.com/minio/selfupdate v0.6.0
	github.com/pkg/sftp v1.13.11
	github.com/xtaci/kcp-go/v5 v5.6.72
	golang.design/x/clipboard v0.9.0
	golang.org/x/crypto v0.55.0
	golang.org/x/sys v0.47.0
	golang.org/x/text v0.41.0
)

require (
	aead.dev/minisign v0.3.0 // indirect
	dario.cat/mergo v1.0.2 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/cloudflare/circl v1.6.5 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/cyphar/filepath-securejoin v0.7.0 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
	github.com/go-git/go-billy/v5 v5.9.1 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/kevinburke/ssh_config v1.6.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/klauspost/reedsolomon v1.14.2 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/pjbgf/sha1cd v0.6.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/skeema/knownhosts v1.3.3 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/u-root/u-root v0.16.0 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	golang.design/x/x11 v0.2.0 // indirect
	golang.org/x/exp/shiny v0.0.0-20260824195058-e88cd73687aa // indirect
	golang.org/x/image v0.45.0 // indirect
	golang.org/x/mobile v0.0.0-20260821190718-4776eadac327 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
)

replace github.com/gliderlabs/ssh => ./internal/sshlib
