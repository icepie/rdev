package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"rdev/internal/agent"
	"testing"
)

func TestThreadPinPersistsAndBroadcastsWithoutChangingHistory(t *testing.T) {
	s := newTurnTestServer(t, "http://127.0.0.1:1")
	entry, err := s.agentSessionFor("dev1", s.clients["dev1"])
	if err != nil {
		t.Fatal(err)
	}
	th := entry.threads[entry.current]
	th.session.SetHistory([]agent.Message{{Role: "user", Content: "retained message"}})
	s.agentStore().save("dev1", th.id, th.model, th.session.History())
	server := httptest.NewServer(http.HandlerFunc(s.HandleAgentWS))
	defer server.Close()
	socket, rec := dialAgentWS(t, server)
	waitAgentMsg(t, rec.msgs, "auth", func(m agentMsg) bool { return m.Op == "auth_ok" })
	_, peer := dialAgentWS(t, server)
	waitAgentMsg(t, peer.msgs, "peer", func(m agentMsg) bool { return m.Op == "auth_ok" })
	request := func(pinned bool) {
		raw, _ := json.Marshal(agentMsg{Op: "pin_thread", Thread: th.id, ID: "pin-request", Pinned: pinned})
		sendAgentOp(t, socket, string(raw))
		ack := waitAgentMsg(t, rec.msgs, "pin", func(m agentMsg) bool { return m.Op == "thread_pinned" })
		if ack.ID != "pin-request" || ack.Pinned != pinned {
			t.Fatal("incorrect acknowledgement", ack)
		}
	}
	request(true)
	update := waitAgentMsg(t, peer.msgs, "pin broadcast", func(m agentMsg) bool { return m.Op == "thread_list" && len(m.Threads) > 0 && m.Threads[0].PinnedAt > 0 })
	stamp := update.Threads[0].PinnedAt
	if s.loadAgentPrefs("dev1").Pinned[th.id] != stamp {
		t.Fatal("pin not persisted")
	}
	request(true)
	if s.loadAgentPrefs("dev1").Pinned[th.id] != stamp {
		t.Fatal("idempotent pin changed order")
	}
	restored := newPrefsTestServer(t)
	restored.DataDir = s.DataDir
	cold, err := restored.agentSessionFor("dev1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cold.threads[th.id].pinnedAt != stamp {
		t.Fatal("cold restart lost pin")
	}
	if got := cold.threads[th.id].session.History(); len(got) != 1 || got[0].Content != "retained message" {
		t.Fatal("pin modified history")
	}
	request(false)
	if len(s.loadAgentPrefs("dev1").Pinned) != 0 {
		t.Fatal("unpin not persisted")
	}
	waitAgentMsg(t, peer.msgs, "unpin broadcast", func(m agentMsg) bool {
		return m.Op == "thread_list" && len(m.Threads) > 0 && m.Threads[0].PinnedAt == 0
	})
}
