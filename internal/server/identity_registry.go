package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type identityRecord struct {
	Identity  string    `json:"identity"`
	ClientID  string    `json:"clientId"`
	ProofHash string    `json:"proofHash"`
	Revoked   bool      `json:"revoked,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	RevokedAt time.Time `json:"revokedAt,omitempty"`
}

type identityAuditRecord struct {
	At               time.Time `json:"at"`
	Action           string    `json:"action"`
	Identity         string    `json:"identity"`
	ClientID         string    `json:"clientId"`
	PreviousClientID string    `json:"previousClientId,omitempty"`
}

var (
	errIdentityNotFound = errors.New("identity not found")
	errClientIDTaken    = errors.New("client ID already in use")
)

type identityRegistry struct {
	mu      sync.Mutex
	loaded  bool
	fault   string
	records map[string]identityRecord
	audit   []identityAuditRecord
}

const (
	maxIdentityRecords       = 100000
	maxIdentityAudit         = 10000
	maxIdentityRegistryBytes = 32 << 20
)

func trimIdentityAudit(reg *identityRegistry) {
	if len(reg.audit) > maxIdentityAudit {
		reg.audit = append([]identityAuditRecord(nil), reg.audit[len(reg.audit)-maxIdentityAudit:]...)
	}
}

type IdentityHealthSnapshot struct {
	Durable          bool `json:"durable"`
	PersistenceFault bool `json:"persistenceFault"`
	Initialized      bool `json:"initialized"`
}

type identityAdminSnapshot struct {
	Devices  []identityRecordView   `json:"devices"`
	Audit    []identityAuditRecord  `json:"audit"`
	Health   IdentityHealthSnapshot `json:"health"`
	OpenMode bool                   `json:"openMode"`
}
type identityRecordView struct {
	Identity  string    `json:"identity"`
	ClientID  string    `json:"clientId"`
	Revoked   bool      `json:"revoked"`
	CreatedAt time.Time `json:"createdAt"`
	RevokedAt time.Time `json:"revokedAt,omitempty"`
}

func (s *Server) identityRegistry() *identityRegistry {
	s.identityOnce.Do(func() {
		if s.identity == nil {
			s.identity = &identityRegistry{records: make(map[string]identityRecord)}
		}
	})
	return s.identity
}

func (s *Server) identityPath() string {
	if strings.TrimSpace(s.DataDir) == "" {
		return ""
	}
	return filepath.Join(s.DataDir, "identity-registry.json")
}

func (s *Server) loadIdentityLocked(reg *identityRegistry) error {
	if reg.loaded {
		if reg.fault != "" {
			return errors.New(reg.fault)
		}
		return nil
	}
	reg.loaded = true
	reg.records = make(map[string]identityRecord)
	path := s.identityPath()
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		reg.fault = err.Error()
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxIdentityRegistryBytes+1))
	if err != nil {
		reg.fault = err.Error()
		return err
	}
	if len(b) > maxIdentityRegistryBytes {
		reg.fault = "identity registry exceeds size limit"
		return errors.New(reg.fault)
	}
	var disk struct {
		Records []identityRecord      `json:"records"`
		Audit   []identityAuditRecord `json:"audit"`
	}
	if err := json.Unmarshal(b, &disk); err != nil {
		reg.fault = err.Error()
		return err
	}
	if len(disk.Records) > maxIdentityRecords || len(disk.Audit) > maxIdentityAudit {
		reg.fault = "identity registry exceeds record limit"
		return errors.New(reg.fault)
	}
	seenClientIDs := make(map[string]struct{}, len(disk.Records))
	for _, record := range disk.Records {
		if len(record.Identity) != 64 || len(record.ProofHash) != 64 || !isLowerHex(record.Identity) || !isLowerHex(record.ProofHash) {
			reg.fault = "invalid identity registry credential"
			return errors.New(reg.fault)
		}
		if !validClientID(record.ClientID) {
			reg.fault = "invalid identity registry client ID"
			return errors.New(reg.fault)
		}
		if _, exists := reg.records[record.Identity]; exists {
			reg.fault = "duplicate identity registry identity"
			return errors.New(reg.fault)
		}
		if _, exists := seenClientIDs[record.ClientID]; exists {
			reg.fault = "duplicate identity registry client ID"
			return errors.New(reg.fault)
		}
		seenClientIDs[record.ClientID] = struct{}{}
		reg.records[record.Identity] = record
	}
	reg.audit = disk.Audit
	return nil
}

func (s *Server) persistIdentityLocked(reg *identityRegistry) error {
	path := s.identityPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		reg.fault = err.Error()
		return err
	}
	records := make([]identityRecord, 0, len(reg.records))
	for _, rec := range reg.records {
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Identity < records[j].Identity })
	b, err := json.MarshalIndent(struct {
		Records []identityRecord      `json:"records"`
		Audit   []identityAuditRecord `json:"audit"`
	}{records, reg.audit}, "", "  ")
	if err != nil {
		reg.fault = err.Error()
		return err
	}
	if len(b) > maxIdentityRegistryBytes {
		reg.fault = "identity registry exceeds size limit"
		return errors.New(reg.fault)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".identity-registry-*")
	if err != nil {
		reg.fault = err.Error()
		return err
	}
	tmp := f.Name()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err == nil {
		err = syncScheduleDirectory(path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		reg.fault = err.Error()
		return err
	}
	reg.fault = ""
	return nil
}

func hashIdentityProof(proof string) string {
	sum := sha256.Sum256([]byte(proof))
	return hex.EncodeToString(sum[:])
}

func validIdentityCredential(identity, proof string) bool {
	return len(identity) == 64 && len(proof) == 64 && isLowerHex(identity) && isLowerHex(proof)
}

func isLowerHex(value string) bool {
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// authenticateRegistration binds first enrollment to a client-persisted proof.
func (s *Server) authenticateRegistration(identity, requestedID, proof string) (string, string, bool, error) {
	reg := s.identityRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if err := s.loadIdentityLocked(reg); err != nil {
		return "", "", false, err
	}
	if !validIdentityCredential(identity, proof) {
		return "", "", false, errors.New("invalid device identity credential")
	}
	if !validClientID(requestedID) {
		return "", "", false, errors.New("invalid requested client ID")
	}
	if existing, ok := reg.records[identity]; ok {
		if existing.Revoked || subtle.ConstantTimeCompare([]byte(existing.ProofHash), []byte(hashIdentityProof(proof))) != 1 {
			return "", "", false, errors.New("invalid device reconnect proof")
		}
		s.mu.RLock()
		_, replacing := s.clients[existing.ClientID]
		s.mu.RUnlock()
		if replacing {
			reg.audit = append(reg.audit, identityAuditRecord{At: time.Now().UTC(), Action: "replacement", Identity: identity, ClientID: existing.ClientID})
			trimIdentityAudit(reg)
			if err := s.persistIdentityLocked(reg); err != nil {
				return "", "", false, err
			}
		}
		return existing.ClientID, "", true, nil
	}
	s.mu.RLock()
	_, occupied := s.clients[requestedID]
	s.mu.RUnlock()
	for _, rec := range reg.records {
		if rec.ClientID == requestedID {
			occupied = true
			break
		}
	}
	assigned := requestedID
	if occupied {
		for n := 2; ; n++ {
			suffix := "-" + fmt.Sprint(n)
			base := requestedID
			if len(base)+len(suffix) > 64 {
				base = base[:64-len(suffix)]
			}
			assigned = base + suffix
			s.mu.RLock()
			_, used := s.clients[assigned]
			s.mu.RUnlock()
			for _, rec := range reg.records {
				if rec.ClientID == assigned {
					used = true
					break
				}
			}
			if !used {
				break
			}
		}
	}
	if len(reg.records) >= maxIdentityRecords {
		return "", "", false, errors.New("identity registry capacity reached")
	}
	rec := identityRecord{Identity: identity, ClientID: assigned, ProofHash: hashIdentityProof(proof), CreatedAt: time.Now().UTC()}
	reg.records[identity] = rec
	reg.audit = append(reg.audit, identityAuditRecord{At: rec.CreatedAt, Action: "registered", Identity: identity, ClientID: assigned})
	trimIdentityAudit(reg)
	if err := s.persistIdentityLocked(reg); err != nil {
		delete(reg.records, identity)
		return "", "", false, err
	}
	return assigned, "", false, nil
}

// renameIdentity changes the stable public device ID. Connected clients are
// disconnected after the durable update so they reconnect through the normal
// registration path and all ID-keyed server state is rebuilt consistently.
func (s *Server) renameIdentity(identity, newClientID string) (*ClientConn, error) {
	if !isLowerHex(identity) || len(identity) != 64 {
		return nil, errIdentityNotFound
	}
	if !validClientID(newClientID) {
		return nil, errors.New("invalid client ID")
	}
	reg := s.identityRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if err := s.loadIdentityLocked(reg); err != nil {
		return nil, err
	}
	rec, ok := reg.records[identity]
	if !ok || rec.Revoked {
		return nil, errIdentityNotFound
	}
	if rec.ClientID == newClientID {
		return nil, nil
	}
	for otherIdentity, other := range reg.records {
		if otherIdentity != identity && other.ClientID == newClientID {
			return nil, errClientIDTaken
		}
	}
	s.mu.Lock()
	if existing := s.clients[newClientID]; existing != nil && existing.DeviceIdentity != identity {
		s.mu.Unlock()
		return nil, errClientIDTaken
	}
	s.mu.Unlock()
	oldClientID := rec.ClientID
	oldAudit := reg.audit
	rec.ClientID = newClientID
	now := time.Now().UTC()
	reg.records[identity] = rec
	reg.audit = append(reg.audit, identityAuditRecord{At: now, Action: "renamed", Identity: identity, ClientID: newClientID, PreviousClientID: oldClientID})
	trimIdentityAudit(reg)
	if err := s.persistIdentityLocked(reg); err != nil {
		rec.ClientID = oldClientID
		reg.records[identity] = rec
		reg.audit = oldAudit
		return nil, err
	}

	s.mu.Lock()
	var client *ClientConn
	if connected := s.clients[oldClientID]; connected != nil && connected.DeviceIdentity == identity {
		client = connected
		delete(s.clients, oldClientID)
		s.notifyDeviceWatchersLocked()
	}
	s.mu.Unlock()
	return client, nil
}

func (s *Server) IdentityHealth() IdentityHealthSnapshot {
	reg := s.identityRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	return IdentityHealthSnapshot{Durable: s.identityPath() != "", PersistenceFault: reg.fault != "", Initialized: reg.loaded}
}
func (s *Server) validateReconnectProof(identity, proof string) bool {
	if !validIdentityCredential(identity, proof) {
		return false
	}
	reg := s.identityRegistry()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if s.loadIdentityLocked(reg) != nil {
		return false
	}
	rec, ok := reg.records[identity]
	return ok && !rec.Revoked && subtle.ConstantTimeCompare([]byte(rec.ProofHash), []byte(hashIdentityProof(proof))) == 1
}

func (s *Server) HandleIdentityAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) || !requireOrigin(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/api/identity" && r.URL.Path != "/api/identity/revoke" && r.URL.Path != "/api/identity/rename" {
		http.NotFound(w, r)
		return
	}
	reg := s.identityRegistry()
	if r.URL.Path == "/api/identity/rename" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var input struct {
			DeviceIdentity string `json:"deviceIdentity"`
			NewClientID    string `json:"newClientId"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil || !isLowerHex(input.DeviceIdentity) || len(input.DeviceIdentity) != 64 || !validClientID(input.NewClientID) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		client, err := s.renameIdentity(input.DeviceIdentity, input.NewClientID)
		if errors.Is(err, errIdentityNotFound) {
			http.Error(w, "identity not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, errClientIDTaken) {
			http.Error(w, "client ID already in use", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "identity registry unavailable", http.StatusServiceUnavailable)
			return
		}
		if client != nil {
			closeClientResources(s, client)
			if client.Transport != nil {
				_ = client.Transport.Close("device ID changed")
			} else if client.Conn != nil {
				_ = client.Conn.WriteClose(1000, []byte("device ID changed"))
			}
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.URL.Path == "/api/identity/revoke" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var input struct {
			DeviceIdentity string `json:"deviceIdentity"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil || !isLowerHex(input.DeviceIdentity) || len(input.DeviceIdentity) != 64 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		reg.mu.Lock()
		if err := s.loadIdentityLocked(reg); err != nil {
			reg.mu.Unlock()
			http.Error(w, "identity registry unavailable", http.StatusServiceUnavailable)
			return
		}
		rec, ok := reg.records[input.DeviceIdentity]
		if !ok {
			reg.mu.Unlock()
			http.Error(w, "identity not found", http.StatusNotFound)
			return
		}
		var persistErr error
		if !rec.Revoked {
			rec.Revoked = true
			rec.RevokedAt = time.Now().UTC()
			reg.records[input.DeviceIdentity] = rec
			reg.audit = append(reg.audit, identityAuditRecord{At: rec.RevokedAt, Action: "revoked", Identity: rec.Identity, ClientID: rec.ClientID})
			trimIdentityAudit(reg)
			persistErr = s.persistIdentityLocked(reg)
		}
		s.mu.Lock()
		var client *ClientConn
		for _, connected := range s.clients {
			if connected.DeviceIdentity == input.DeviceIdentity {
				client = connected
				delete(s.clients, connected.ID)
				break
			}
		}
		if client != nil {
			s.notifyDeviceWatchersLocked()
		}
		s.mu.Unlock()
		reg.mu.Unlock()
		if client != nil {
			closeClientResources(s, client)
			if client.Transport != nil {
				_ = client.Transport.Close("device identity revoked")
			} else if client.Conn != nil {
				_ = client.Conn.WriteClose(1008, []byte("device identity revoked"))
			}
		}
		if persistErr != nil {
			http.Error(w, "identity registry unavailable; device disconnected but durable revocation failed", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	reg.mu.Lock()
	err := s.loadIdentityLocked(reg)
	snap := identityAdminSnapshot{Devices: []identityRecordView{}, Audit: append([]identityAuditRecord{}, reg.audit...), Health: IdentityHealthSnapshot{Durable: s.identityPath() != "", PersistenceFault: reg.fault != "", Initialized: reg.loaded}, OpenMode: s.AdminToken == ""}
	if err == nil {
		for _, rec := range reg.records {
			snap.Devices = append(snap.Devices, identityRecordView{Identity: rec.Identity, ClientID: rec.ClientID, Revoked: rec.Revoked, CreatedAt: rec.CreatedAt, RevokedAt: rec.RevokedAt})
		}
		sort.Slice(snap.Devices, func(i, j int) bool { return snap.Devices[i].ClientID < snap.Devices[j].ClientID })
	}
	reg.mu.Unlock()
	if err != nil {
		http.Error(w, "identity registry unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(snap)
}
