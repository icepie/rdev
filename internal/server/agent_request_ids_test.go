package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lxzan/gws"
)

// Exercise the wire dispatcher and error/empty-result responses, without
// executing commands on the server host or requiring a real device.
func TestAgentPanelRequestIDs(t *testing.T) {
	srv := newPrefsTestServer(t)
	t.Cleanup(srv.waitAgentWork)
	server := httptest.NewServer(http.HandlerFunc(srv.HandleAgentWS))
	defer server.Close()
	rec := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 128)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{
		Addr:             "ws" + strings.TrimPrefix(server.URL, "http") + "/agent?device=%40server",
		HandshakeTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	waitRawMsg(t, rec.msgs, "auth", isOp("auth_ok"))
	// Drain the initial automatic changes push before testing legacy replies.
	waitRawMsg(t, rec.msgs, "initial changes", isOp("changes"))
	for _, id := range []string{"request-1", "request-2", ""} {
		for _, tc := range []struct{ op, reply, path, query string }{
			{"get_changes", "changes", "", ""},
			{"get_diff", "diff", "", ""},
			{"get_diff", "diff", "same.txt", ""},
			{"file_read", "file_content", "", ""},
			{"file_read", "file_content", "same.txt", ""},
			{"dir_list", "dir_entries", "", ""},
			{"dir_mkdir", "dir_mkdir", "", ""},
			{"search_files", "file_search", "", ""},
			{"search_files", "file_search", "", "same"},
		} {
			t.Run(tc.op+"/"+id+"/"+tc.path+tc.query, func(t *testing.T) {
				body, err := json.Marshal(agentMsg{Op: tc.op, ID: id, Path: tc.path, Q: tc.query})
				if err != nil {
					t.Fatal(err)
				}
				sendAgentOp(t, socket, string(body))
				reply := waitRawMsg(t, rec.msgs, tc.reply, isOp(tc.reply))
				if got := rawString(t, reply, "id"); got != id {
					t.Fatalf("response ID = %q, want %q", got, id)
				}
			})
		}
	}
}
