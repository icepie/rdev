package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/tickstep/aliyunpan-api/aliyunpan"
	"github.com/tickstep/aliyunpan-api/aliyunpan_open"
	"github.com/tickstep/aliyunpan-api/aliyunpan_open/openapi"
	"rdev/internal/protocol"
)

const defaultAliyunPanDeviceID = "aliyunpan"

type AliyunPanConfig struct {
	Enabled     bool   `json:"enabled"`
	DeviceID    string `json:"deviceId"`
	ConfigPath  string `json:"configPath"`
	Password    string `json:"password"`
	TempDir     string `json:"tmpDir"`
	TransferDir string `json:"transferDir"`
	Root        string `json:"root"`
	DriveID     string `json:"driveId"`
}

type aliyunPanCredential struct {
	ActiveUID    string `json:"activeUID"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	UserList     []struct {
		UserID        string `json:"userId"`
		ActiveDriveID string `json:"activeDriveId"`
		TicketID      string `json:"ticketId"`
		OpenapiToken  *struct {
			AccessToken string `json:"accessToken"`
			Expired     int64  `json:"expired"`
		} `json:"openapiToken"`
	} `json:"userList"`
}

type AliyunPanBackend struct {
	id          string
	label       string
	password    string
	configPath  string
	root        string
	tempDir     string
	transferDir string
	driveID     string
	client      *aliyunpan_open.OpenPanClient
	credential  aliyunPanCredential
	mu          sync.Mutex
}

func NewAliyunPanBackend(configPath string, cfg AliyunPanConfig) (*AliyunPanBackend, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var credential aliyunPanCredential
	if err := json.Unmarshal(data, &credential); err != nil {
		return nil, err
	}
	var user *struct {
		UserID        string `json:"userId"`
		ActiveDriveID string `json:"activeDriveId"`
		TicketID      string `json:"ticketId"`
		OpenapiToken  *struct {
			AccessToken string `json:"accessToken"`
			Expired     int64  `json:"expired"`
		} `json:"openapiToken"`
	}
	for i := range credential.UserList {
		if credential.ActiveUID == "" || credential.UserList[i].UserID == credential.ActiveUID {
			user = &credential.UserList[i]
			break
		}
	}
	if user == nil || user.OpenapiToken == nil || user.OpenapiToken.AccessToken == "" {
		return nil, errors.New("aliyunpan openapi token not found in config")
	}
	driveID := cfg.DriveID
	if driveID == "" {
		driveID = user.ActiveDriveID
	}
	if driveID == "" {
		return nil, errors.New("aliyunpan drive id is empty")
	}
	id := cfg.DeviceID
	if id == "" {
		id = defaultAliyunPanDeviceID
	}
	root := cleanPanPath(cfg.Root)
	transferDir := cleanPanPath(cfg.TransferDir)
	if transferDir == "/" {
		transferDir = "/rdev-transfer"
	}
	if cfg.TempDir != "" {
		if err := os.MkdirAll(cfg.TempDir, 0700); err != nil {
			return nil, err
		}
	}
	client := aliyunpan_open.NewOpenPanClient(openapi.ApiConfig{
		TicketId:     user.TicketID,
		UserId:       user.UserID,
		ClientId:     credential.ClientID,
		ClientSecret: credential.ClientSecret,
	}, openapi.ApiToken{AccessToken: user.OpenapiToken.AccessToken, ExpiredAt: user.OpenapiToken.Expired}, nil)
	backend := &AliyunPanBackend{id: id, label: "AliyunPan", password: cfg.Password, configPath: configPath, root: root, tempDir: cfg.TempDir, transferDir: transferDir, driveID: driveID, client: client, credential: credential}
	client.SetAccessTokenRefreshCallback(func(userID string, token openapi.ApiToken) error {
		return backend.saveToken(userID, token)
	})
	return backend, nil
}

func LoadAliyunPanConfig(path string) (AliyunPanConfig, error) {
	var cfg AliyunPanConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (b *AliyunPanBackend) ID() string          { return b.id }
func (b *AliyunPanBackend) Label() string       { return b.label }
func (b *AliyunPanBackend) Password() string    { return b.password }
func (b *AliyunPanBackend) TransferDir() string { return b.transferDir }

func (b *AliyunPanBackend) List(ctx context.Context, reqPath string) ([]protocol.FileEntry, string, string, bool, error) {
	panPath := b.panPath(reqPath)
	info, err := b.fileInfo(panPath)
	if err != nil {
		return nil, panPath, b.root, false, err
	}
	if !info.IsFolder() {
		return []protocol.FileEntry{b.entry(info)}, path.Dir(panPath), b.root, false, nil
	}
	files, apiErr := b.client.FileListGetAll(&aliyunpan.FileListParam{DriveId: b.driveID, ParentFileId: info.FileId, Limit: 200, OrderBy: aliyunpan.FileOrderByName, OrderDirection: aliyunpan.FileOrderDirectionAsc}, 0)
	if apiErr != nil {
		return nil, panPath, b.root, false, apiErr
	}
	entries := make([]protocol.FileEntry, 0, len(files))
	for _, file := range files {
		if file != nil {
			entries = append(entries, b.entry(file))
		}
	}
	return entries, panPath, b.root, false, nil
}

func (b *AliyunPanBackend) OpenUpload(ctx context.Context, reqPath, parentPath, name string, size int64, modTime string) (backendUpload, error) {
	target := b.uploadTarget(reqPath, parentPath, name)
	tmp, err := os.CreateTemp(b.tempDir, "rdev-aliyunpan-upload-*")
	if err != nil {
		return nil, err
	}
	return &aliyunPanUpload{backend: b, target: target, file: tmp, tmpPath: tmp.Name(), size: size}, nil
}

func (b *AliyunPanBackend) OpenTransferUpload(ctx context.Context, taskID, name string, size int64, modTime string) (backendUpload, error) {
	if name == "" || name == "." || name == "/" {
		name = "upload.bin"
	}
	target := cleanPanPath(path.Join(b.transferDir, taskID, path.Base(name)))
	tmp, err := os.CreateTemp(b.tempDir, "rdev-aliyunpan-upload-*")
	if err != nil {
		return nil, err
	}
	return &aliyunPanUpload{backend: b, target: target, file: tmp, tmpPath: tmp.Name(), size: size}, nil
}

func (b *AliyunPanBackend) Download(ctx context.Context, reqPath string, offset int64, w io.Writer) (backendDownloadInfo, error) {
	panPath := b.panPath(reqPath)
	info, err := b.fileInfo(panPath)
	if err != nil {
		return backendDownloadInfo{}, err
	}
	if info.IsFolder() {
		return backendDownloadInfo{}, errors.New("cannot download directory")
	}
	urlInfo, directURL, err := b.DownloadURL(ctx, reqPath)
	if err != nil {
		return backendDownloadInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, directURL, nil)
	if err != nil {
		return backendDownloadInfo{}, err
	}
	req.Header.Set("user-agent", "Mozilla/5.0 RDev")
	req.Header.Set("referer", "https://www.aliyundrive.com/")
	if offset > 0 {
		req.Header.Set("range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return backendDownloadInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return backendDownloadInfo{}, fmt.Errorf("download failed: %s", resp.Status)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return backendDownloadInfo{}, err
	}
	return urlInfo, nil
}

func (b *AliyunPanBackend) DownloadURL(ctx context.Context, reqPath string) (backendDownloadInfo, string, error) {
	return b.DownloadURLAbsolute(ctx, b.panPath(reqPath))
}

func (b *AliyunPanBackend) DownloadURLAbsolute(ctx context.Context, panPath string) (backendDownloadInfo, string, error) {
	panPath = cleanPanPath(panPath)
	info, err := b.fileInfo(panPath)
	if err != nil {
		return backendDownloadInfo{}, "", err
	}
	if info.IsFolder() {
		return backendDownloadInfo{}, "", errors.New("cannot download directory")
	}
	url, apiErr := b.client.GetFileDownloadUrl(&aliyunpan.GetFileDownloadUrlParam{DriveId: b.driveID, FileId: info.FileId, ExpireSec: 14400})
	if apiErr != nil {
		return backendDownloadInfo{}, "", apiErr
	}
	return backendDownloadInfo{Path: panPath, Name: info.FileName, Size: info.FileSize, Offset: 0, ModTime: parsePanTime(info.UpdatedAt)}, url.Url, nil
}

func (b *AliyunPanBackend) Stat(ctx context.Context, reqPath string) (os.FileInfo, error) {
	info, err := b.fileInfo(b.panPath(reqPath))
	if err != nil {
		return nil, err
	}
	return panFileInfo{entry: info}, nil
}

func (b *AliyunPanBackend) ReadFile(ctx context.Context, reqPath string) (io.ReaderAt, os.FileInfo, func() error, error) {
	tmp, err := os.CreateTemp(b.tempDir, "rdev-aliyunpan-download-*")
	if err != nil {
		return nil, nil, nil, err
	}
	info, err := b.Download(ctx, reqPath, 0, tmp)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, nil, nil, err
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, nil, nil, err
	}
	closeFn := func() error {
		name := tmp.Name()
		err := tmp.Close()
		os.Remove(name)
		return err
	}
	return tmp, staticFileInfo{name: info.Name, size: info.Size, modTime: info.ModTime}, closeFn, nil
}

func (b *AliyunPanBackend) WriteFile(ctx context.Context, reqPath string) (io.WriterAt, func() error, error) {
	upload, err := b.OpenUpload(ctx, reqPath, "", "", 0, "")
	if err != nil {
		return nil, nil, err
	}
	closeFn := func() error {
		_, err := upload.Commit(ctx)
		return err
	}
	return upload, closeFn, nil
}

func (b *AliyunPanBackend) Mkdir(ctx context.Context, reqPath string) error {
	_, apiErr := b.client.MkdirByFullPath(b.driveID, b.panPath(reqPath))
	if apiErr != nil {
		return apiErr
	}
	return nil
}

func (b *AliyunPanBackend) Remove(ctx context.Context, reqPath string) error {
	info, err := b.fileInfo(b.panPath(reqPath))
	if err != nil {
		return err
	}
	_, apiErr := b.client.FileDelete(&aliyunpan.FileBatchActionParam{DriveId: b.driveID, FileId: info.FileId})
	if apiErr != nil {
		return apiErr
	}
	return nil
}

func (b *AliyunPanBackend) Rename(ctx context.Context, oldPath, newPath string) error {
	oldPanPath := b.panPath(oldPath)
	newPanPath := b.panPath(newPath)
	info, err := b.fileInfo(oldPanPath)
	if err != nil {
		return err
	}
	newParent := path.Dir(newPanPath)
	if newParent != path.Dir(oldPanPath) {
		return errors.New("aliyunpan rename across directories is not supported yet")
	}
	ok, apiErr := b.client.FileRename(b.driveID, info.FileId, path.Base(newPanPath))
	if apiErr != nil {
		return apiErr
	}
	if !ok {
		return errors.New("rename failed")
	}
	return nil
}

func (b *AliyunPanBackend) uploadFile(ctx context.Context, localPath, panPath string, expectedSize int64) (int64, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if expectedSize > 0 && st.Size() != expectedSize {
		return 0, fmt.Errorf("size mismatch: wrote %d of %d", st.Size(), expectedSize)
	}
	parentPath := path.Dir(panPath)
	parent, err := b.ensureDir(parentPath)
	if err != nil {
		return 0, err
	}
	hash, err := sha1File(file)
	if err != nil {
		return 0, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return 0, err
	}
	parts := aliyunpan.GenerateFileUploadPartInfoList(st.Size())
	created, apiErr := b.client.CreateUploadFile(&aliyunpan.CreateFileUploadParam{DriveId: b.driveID, ParentFileId: parent.FileId, Name: path.Base(panPath), Size: st.Size(), PartInfoList: parts, ContentHash: strings.ToUpper(hash), ContentHashName: "sha1", Type: "file", CheckNameMode: "overwrite"})
	if apiErr != nil {
		return 0, apiErr
	}
	if created.RapidUpload {
		return st.Size(), nil
	}
	for i, part := range created.PartInfoList {
		start := int64(i) * aliyunpan.DefaultChunkSize
		length := aliyunpan.DefaultChunkSize
		if remaining := st.Size() - start; remaining < length {
			length = remaining
		}
		section := io.NewSectionReader(file, start, length)
		respErr := b.client.UploadFileData(part.UploadURL, func(method, url string, headers map[string]string) (*http.Response, error) {
			req, err := http.NewRequestWithContext(ctx, method, url, section)
			if err != nil {
				return nil, err
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			req.ContentLength = length
			return http.DefaultClient.Do(req)
		})
		if respErr != nil {
			return 0, respErr
		}
	}
	_, apiErr = b.client.CompleteUploadFile(&aliyunpan.CompleteUploadFileParam{DriveId: b.driveID, FileId: created.FileId, UploadId: created.UploadId})
	if apiErr != nil {
		return 0, apiErr
	}
	return st.Size(), nil
}

func (b *AliyunPanBackend) fileInfo(panPath string) (*aliyunpan.FileEntity, error) {
	panPath = cleanPanPath(panPath)
	if panPath == "/" {
		return aliyunpan.NewFileEntityForRootDir(), nil
	}
	info, apiErr := b.client.FileInfoByPath(b.driveID, panPath)
	if apiErr != nil {
		return nil, apiErr
	}
	return info, nil
}

func (b *AliyunPanBackend) ensureDir(panPath string) (*aliyunpan.FileEntity, error) {
	panPath = cleanPanPath(panPath)
	if panPath == "/" {
		return aliyunpan.NewFileEntityForRootDir(), nil
	}
	if info, err := b.fileInfo(panPath); err == nil && info.IsFolder() {
		return info, nil
	}
	if _, apiErr := b.client.MkdirByFullPath(b.driveID, panPath); apiErr != nil {
		return nil, apiErr
	}
	return b.fileInfo(panPath)
}

func (b *AliyunPanBackend) panPath(reqPath string) string {
	reqPath = cleanPanPath(reqPath)
	if b.root == "/" {
		return reqPath
	}
	if reqPath == "/" {
		return b.root
	}
	return cleanPanPath(path.Join(b.root, strings.TrimPrefix(reqPath, "/")))
}

func (b *AliyunPanBackend) uploadTarget(reqPath, parentPath, name string) string {
	if reqPath != "" && reqPath != "/" {
		return b.panPath(reqPath)
	}
	if name == "" {
		name = path.Base(reqPath)
	}
	return b.panPath(path.Join(parentPath, name))
}

func (b *AliyunPanBackend) entry(file *aliyunpan.FileEntity) protocol.FileEntry {
	filePath := file.Path
	if filePath == "" || filePath == "/" {
		filePath = path.Join(b.root, file.FileName)
	}
	if b.root != "/" {
		filePath = "/" + strings.TrimPrefix(strings.TrimPrefix(filePath, b.root), "/")
		if filePath == "/" && file.FileName != "" {
			filePath = "/" + file.FileName
		}
	}
	return protocol.FileEntry{Name: file.FileName, Path: cleanPanPath(filePath), IsDir: file.IsFolder(), Size: file.FileSize, ModTime: parsePanTime(file.UpdatedAt).Format(time.RFC3339)}
}

func (b *AliyunPanBackend) saveToken(userID string, token openapi.ApiToken) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.credential.UserList {
		if b.credential.UserList[i].UserID == userID && b.credential.UserList[i].OpenapiToken != nil {
			b.credential.UserList[i].OpenapiToken.AccessToken = token.AccessToken
			b.credential.UserList[i].OpenapiToken.Expired = token.ExpiredAt
		}
	}
	data, err := json.MarshalIndent(b.credential, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.configPath, data, 0600)
}

type aliyunPanUpload struct {
	backend *AliyunPanBackend
	target  string
	file    *os.File
	tmpPath string
	size    int64
	offset  int64
	closed  bool
}

func (u *aliyunPanUpload) Path() string  { return u.target }
func (u *aliyunPanUpload) Offset() int64 { return u.offset }

func (u *aliyunPanUpload) WriteAt(p []byte, off int64) (int, error) {
	n, err := u.file.WriteAt(p, off)
	if next := off + int64(n); next > u.offset {
		u.offset = next
	}
	return n, err
}

func (u *aliyunPanUpload) Commit(ctx context.Context) (int64, error) {
	if u.closed {
		return u.offset, nil
	}
	u.closed = true
	if err := u.file.Sync(); err != nil {
		u.Cancel()
		return 0, err
	}
	if err := u.file.Close(); err != nil {
		u.Cancel()
		return 0, err
	}
	defer os.Remove(u.tmpPath)
	return u.backend.uploadFile(ctx, u.tmpPath, u.target, u.size)
}

func (u *aliyunPanUpload) Cancel() error {
	if !u.closed {
		u.closed = true
		_ = u.file.Close()
	}
	return os.Remove(u.tmpPath)
}

type panFileInfo struct{ entry *aliyunpan.FileEntity }

func (p panFileInfo) Name() string { return p.entry.FileName }
func (p panFileInfo) Size() int64  { return p.entry.FileSize }
func (p panFileInfo) Mode() os.FileMode {
	if p.entry.IsFolder() {
		return os.ModeDir | 0755
	}
	return 0644
}
func (p panFileInfo) ModTime() time.Time { return parsePanTime(p.entry.UpdatedAt) }
func (p panFileInfo) IsDir() bool        { return p.entry.IsFolder() }
func (p panFileInfo) Sys() any           { return nil }

type staticFileInfo struct {
	name    string
	size    int64
	modTime time.Time
}

func (s staticFileInfo) Name() string       { return s.name }
func (s staticFileInfo) Size() int64        { return s.size }
func (s staticFileInfo) Mode() os.FileMode  { return 0644 }
func (s staticFileInfo) ModTime() time.Time { return s.modTime }
func (s staticFileInfo) IsDir() bool        { return false }
func (s staticFileInfo) Sys() any           { return nil }

func cleanPanPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return path.Clean(value)
}

func sha1File(file *os.File) (string, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return "", err
	}
	h := sha1.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func parsePanTime(value string) time.Time {
	if value == "" {
		return time.Now()
	}
	formats := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"}
	for _, layout := range formats {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Now()
}

var _ fileBackend = (*AliyunPanBackend)(nil)
var _ transferUploadBackend = (*AliyunPanBackend)(nil)
var _ absolutePathBackend = (*AliyunPanBackend)(nil)
var _ backendUpload = (*aliyunPanUpload)(nil)
var _ os.FileInfo = panFileInfo{}
var _ os.FileInfo = staticFileInfo{}
