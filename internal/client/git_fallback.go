package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	shlex "github.com/anmitsu/go-shlex"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitserver "github.com/go-git/go-git/v5/plumbing/transport/server"
	"rdev/internal/protocol"
)

const (
	gitSnapshotMaxFiles     = 20000
	gitSnapshotMaxTotalSize = 512 * 1024 * 1024
	gitSnapshotMaxFileSize  = 64 * 1024 * 1024
)

type gitSmartCommand struct {
	Name string
	Path string
}

func parseGitSmartSSHCommand(command string) (gitSmartCommand, bool) {
	fields, err := shlex.Split(command, true)
	if err != nil || len(fields) != 2 {
		return gitSmartCommand{}, false
	}
	name := strings.Trim(fields[0], `"'`)
	switch name {
	case "git-upload-pack", "git-receive-pack", "git-upload-archive":
	default:
		return gitSmartCommand{}, false
	}
	path := fields[1]
	if path == "" || strings.ContainsRune(path, 0) {
		return gitSmartCommand{}, false
	}
	return gitSmartCommand{Name: name, Path: path}, true
}

func hasSystemGitCommand(name string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath(name + ".exe"); err == nil {
			return true
		}
	}
	_, err := exec.LookPath("git")
	return err == nil
}

type nopWriteCloser struct {
	io.Writer
}

func (w nopWriteCloser) Close() error { return nil }

func (c *Client) startGitFallbackSession(sess *clientSession, cmd gitSmartCommand) (*clientSession, error) {
	pr, pw := io.Pipe()
	sess.stdinPipe = pw

	go func() {
		exitCode := 0
		if err := c.serveGitFallback(cmd, pr, newCoalescingWriter(c, sess.id, protocol.BinData), newCoalescingWriter(c, sess.id, protocol.BinStderr)); err != nil {
			exitCode = 1
			c.sendBinary(protocol.BinStderr, sess.id, []byte("rdev git fallback: "+err.Error()+"\n"))
		}
		pr.Close()
		c.sendExitCode(sess.id, exitCode)
		c.sendClose(sess.id)
		sess.close()
	}()

	return sess, nil
}

func (c *Client) serveGitFallback(cmd gitSmartCommand, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	switch cmd.Name {
	case "git-upload-pack":
		session, cleanup, err := newGitUploadPackSession(cmd.Path)
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return err
		}
		return serveUploadPack(stdin, nopWriteCloser{stdout}, session)
	case "git-receive-pack":
		session, err := newGitReceivePackSession(cmd.Path)
		if err != nil {
			return err
		}
		return serveReceivePack(stdin, nopWriteCloser{stdout}, session)
	case "git-upload-archive":
		return errors.New("git-upload-archive is not supported without system git")
	default:
		return fmt.Errorf("unsupported git command %q", cmd.Name)
	}
}

func newGitUploadPackSession(path string) (transport.UploadPackSession, func(), error) {
	repo, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
	if err == nil {
		session, err := newUploadPackSessionFromRepo(repo, path)
		return session, nil, err
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, nil, err
	}

	tmp, err := os.MkdirTemp("", "rdev-git-snapshot-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	barePath := filepath.Join(tmp, "snapshot.git")
	if err := buildDirectorySnapshotRepo(path, barePath); err != nil {
		cleanup()
		return nil, nil, err
	}
	repo, err = git.PlainOpen(barePath)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	session, err := newUploadPackSessionFromRepo(repo, barePath)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return session, cleanup, nil
}

func newGitReceivePackSession(path string) (transport.ReceivePackSession, error) {
	repo, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, errors.New("push requires a bare Git repository when device git is unavailable")
		}
		return nil, err
	}
	cfg, err := repo.Config()
	if err != nil {
		return nil, err
	}
	if !cfg.Core.IsBare {
		return nil, errors.New("push requires a bare Git repository when device git is unavailable")
	}
	return newReceivePackSessionFromRepo(repo, path)
}

func newUploadPackSessionFromRepo(repo *git.Repository, path string) (transport.UploadPackSession, error) {
	ep := endpointForPath(path)
	server := gitserver.NewServer(gitserver.MapLoader{ep.String(): repo.Storer})
	return server.NewUploadPackSession(ep, nil)
}

func newReceivePackSessionFromRepo(repo *git.Repository, path string) (transport.ReceivePackSession, error) {
	ep := endpointForPath(path)
	server := gitserver.NewServer(gitserver.MapLoader{ep.String(): repo.Storer})
	return server.NewReceivePackSession(ep, nil)
}

func endpointForPath(path string) *transport.Endpoint {
	return &transport.Endpoint{Protocol: "file", Path: loaderKey(path)}
}

func loaderKey(path string) string {
	return filepath.ToSlash(path)
}

func buildDirectorySnapshotRepo(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}

	workDir := dst + ".work"
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	repo, err := git.PlainInit(workDir, false)
	if err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}

	stats := &snapshotStats{}
	if err := copySnapshotTree(src, workDir, stats); err != nil {
		return err
	}
	if _, err := wt.Add("."); err != nil {
		return err
	}
	_, err = wt.Commit("snapshot: "+src, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "rdev",
			Email: "rdev@local",
			When:  time.Now(),
		},
	})
	if err != nil {
		return err
	}
	_, err = git.PlainClone(dst, true, &git.CloneOptions{URL: workDir})
	return err
}

type snapshotStats struct {
	files int
	size  int64
}

func copySnapshotTree(src, dst string, stats *snapshotStats) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		stats.files++
		stats.size += info.Size()
		if stats.files > gitSnapshotMaxFiles {
			return fmt.Errorf("directory snapshot has too many files, limit is %d", gitSnapshotMaxFiles)
		}
		if info.Size() > gitSnapshotMaxFileSize {
			return fmt.Errorf("file %s exceeds snapshot file size limit", rel)
		}
		if stats.size > gitSnapshotMaxTotalSize {
			return fmt.Errorf("directory snapshot exceeds total size limit")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func serveUploadPack(stdin io.Reader, stdout io.WriteCloser, session transport.UploadPackSession) (err error) {
	defer stdout.Close()
	ar, err := session.AdvertisedReferences()
	if err != nil {
		return err
	}
	if err := ar.Encode(stdout); err != nil {
		return err
	}
	req := packp.NewUploadPackRequest()
	if err := req.Decode(stdin); err != nil {
		return err
	}
	resp, err := session.UploadPack(context.TODO(), req)
	if err != nil {
		return err
	}
	return resp.Encode(stdout)
}

func serveReceivePack(stdin io.Reader, stdout io.WriteCloser, session transport.ReceivePackSession) error {
	ar, err := session.AdvertisedReferences()
	if err != nil {
		return fmt.Errorf("internal error in advertised references: %s", err)
	}
	if err := ar.Encode(stdout); err != nil {
		return fmt.Errorf("error in advertised references encoding: %s", err)
	}
	req := packp.NewReferenceUpdateRequest()
	if err := req.Decode(stdin); err != nil {
		return fmt.Errorf("error decoding: %s", err)
	}
	report, err := session.ReceivePack(context.TODO(), req)
	if report != nil {
		if encErr := report.Encode(stdout); encErr != nil {
			return fmt.Errorf("error in encoding report status %s", encErr)
		}
	}
	if err != nil {
		return fmt.Errorf("error in receive pack: %s", err)
	}
	return nil
}
