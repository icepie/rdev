module rdev

go 1.26.2

require (
	github.com/BurntSushi/xgb v0.0.0-20210121224620-deaf085860bc
	github.com/MoeForge/moejs v0.1.0
	github.com/anmitsu/go-shlex v0.0.0-20200514113438-38f4b401e2be
	github.com/aymanbagabas/go-pty v0.2.3
	github.com/ebitengine/purego v0.11.3
	github.com/gen2brain/x264-go v0.4.0
	github.com/gliderlabs/ssh v0.3.8
	github.com/go-git/go-git/v5 v5.19.3
	github.com/gofrs/flock v0.13.2
	github.com/iamacarpet/go-winpty v1.0.4
	github.com/lxzan/gws v1.10.2
	github.com/minio/selfupdate v0.6.0
	github.com/pion/webrtc/v4 v4.2.26
	github.com/pkg/sftp v1.13.12
	github.com/robfig/cron/v3 v3.0.1
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/xtaci/kcp-go/v5 v5.7.5
	golang.design/x/clipboard v0.11.0
	golang.org/x/crypto v0.59.0
	golang.org/x/image v0.48.0
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.50.0
	golang.org/x/text v0.44.0
)

require (
	aead.dev/minisign v0.3.0 // indirect
	dario.cat/mergo v1.0.2 // indirect
	github.com/Microsoft/go-winio v0.6.4 // indirect
	github.com/MoeForge/moego v0.1.0-alpha.2 // indirect
	github.com/ProtonMail/go-crypto v1.5.2 // indirect
	github.com/cloudflare/circl v1.6.5 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/cyphar/filepath-securejoin v0.7.0 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
	github.com/go-git/go-billy/v5 v5.9.2 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/juju/ratelimit v1.0.2 // indirect
	github.com/kevinburke/ssh_config v1.6.0 // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/klauspost/cpuid/v2 v2.4.2 // indirect
	github.com/klauspost/reedsolomon v1.14.2 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/pion/datachannel v1.6.4 // indirect
	github.com/pion/dtls/v3 v3.1.15 // indirect
	github.com/pion/ice/v4 v4.4.9 // indirect
	github.com/pion/interceptor v0.1.51 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.2.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.20 // indirect
	github.com/pion/rtp v1.10.6 // indirect
	github.com/pion/sctp v1.12.1 // indirect
	github.com/pion/sdp/v3 v3.0.20 // indirect
	github.com/pion/srtp/v3 v3.1.3 // indirect
	github.com/pion/stun/v4 v4.0.1 // indirect
	github.com/pion/transport/v5 v5.1.5 // indirect
	github.com/pion/turn/v5 v5.1.3 // indirect
	github.com/pjbgf/sha1cd v0.7.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/skeema/knownhosts v1.3.3 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	github.com/u-root/u-root v0.16.0 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	golang.design/x/x11 v0.2.0 // indirect
	golang.org/x/exp/shiny v0.0.0-20261112173441-d10d3b464990 // indirect
	golang.org/x/mobile v0.0.0-20261011034528-197770444867 // indirect
	golang.org/x/time v0.16.0 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
)

replace github.com/gliderlabs/ssh => ./internal/sshlib
