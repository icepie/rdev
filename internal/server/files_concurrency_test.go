package server

import (
	"testing"
	"time"

	"rdev/internal/protocol"
)

func TestClosingFileSocketDoesNotBlockOtherWorkspaces(t *testing.T) {
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	transport := &fakeDeviceTransport{onSend: func(msg *protocol.Message) {
		if msg.Type != protocol.MsgFileTransferCancel || msg.TaskID != "closing-task" {
			t.Errorf("unexpected cancellation: %+v", msg)
		}
		close(entered)
		<-release
	}}
	closing, other := &fileSocket{}, &fileSocket{}
	srv := &Server{
		clients:      map[string]*ClientConn{"slow": {ID: "slow", Transport: transport}},
		fileRequests: map[string]*fileSocket{"closing-list": closing, "other-list": other},
		fileTasks: map[string]*fileTaskRoute{
			"closing-task": {socket: closing, deviceID: "slow"},
			"other-task":   {socket: other, deviceID: "other"},
		},
	}
	go func() { srv.closeFileSocket(closing); close(closed) }()
	defer func() {
		close(release)
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Error("cleanup did not finish")
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach transport")
	}
	checked := make(chan bool, 1)
	go func() {
		srv.registerFileTask("new-task", other, "other")
		srv.fileMu.RLock()
		valid := srv.fileTasks["closing-task"] == nil && srv.fileRequests["closing-list"] == nil &&
			srv.fileTasks["other-task"].socket == other && srv.fileRequests["other-list"] == other &&
			srv.fileTasks["new-task"].socket == other
		srv.fileMu.RUnlock()
		checked <- valid
	}()
	select {
	case valid := <-checked:
		if !valid {
			t.Fatal("closing removed another workspace or retained its own routes")
		}
	case <-time.After(time.Second):
		t.Fatal("slow device blocked unrelated file routing")
	}
}
