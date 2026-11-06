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

func TestFileErrorsIdentifyOriginalOperation(t *testing.T) {
	srv := &Server{clients: map[string]*ClientConn{"locked": {ID: "locked", Password: "test-password"}}}
	server := httptest.NewServer(http.HandlerFunc(srv.HandleFilesWS))
	defer server.Close()
	rec := &rawWSRecorder{msgs: make(chan map[string]json.RawMessage, 16)}
	socket, _, err := gws.NewClient(rec, &gws.ClientOption{Addr: "ws" + strings.TrimPrefix(server.URL, "http"), HandshakeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.WriteClose(1000, nil)
	go socket.ReadLoop()
	for _, device := range []string{"offline", "locked"} {
		for _, op := range []string{"list", "upload_start", "download_start", "stat", "mkdir"} {
			request := fileMsg{Op: op, DeviceID: device}
			if strings.HasSuffix(op, "_start") {
				request.TaskID = device + "-" + op
			} else {
				request.RequestID = device + "-" + op
			}
			raw, _ := json.Marshal(request)
			if err := socket.WriteMessage(gws.OpcodeText, raw); err != nil {
				t.Fatal(err)
			}
			select {
			case frame := <-rec.msgs:
				raw, _ := json.Marshal(frame)
				var response fileMsg
				if err := json.Unmarshal(raw, &response); err != nil {
					t.Fatal(err)
				}
				wantOp := "error"
				if device == "locked" {
					wantOp = "auth_required"
				}
				if response.Op != wantOp || response.DeviceID != device || response.TaskID != request.TaskID || response.RequestID != request.RequestID || response.Message == "" {
					t.Fatalf("request %+v: wrong response %+v", request, response)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("no response for %+v", request)
			}
		}
	}
}
