package server

import "testing"

func TestAllocateClientIDLockedUsesSuffixOnConflict(t *testing.T) {
	s := NewServer()
	s.clients["device"] = &ClientConn{ID: "device"}

	if got := s.allocateClientIDLocked("other"); got != "other" {
		t.Fatalf("expected free ID to be unchanged, got %q", got)
	}
	if got := s.allocateClientIDLocked("device"); got != "device-2" {
		t.Fatalf("expected first conflicting ID to use -2 suffix, got %q", got)
	}

	s.clients["device-2"] = &ClientConn{ID: "device-2"}
	s.clients["device-3"] = &ClientConn{ID: "device-3"}
	if got := s.allocateClientIDLocked("device"); got != "device-4" {
		t.Fatalf("expected suffix allocation to skip occupied IDs, got %q", got)
	}
}
