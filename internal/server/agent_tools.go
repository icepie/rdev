package server

import (
	"log"

	"rdev/internal/agent"
	"rdev/internal/protocol"
)

// --- Agent file-op routing glue ---
//
// Agent tools run file operations on a device through the same protocol the
// browser file manager uses, but results must come back to a Go channel, not
// a browser socket. Agent requests/tasks live in their own maps; the device
// message dispatch (handleFileManagerMessage / handleFileManagerBinary in
// files.go) consults them first via routeAgentFileMsg/routeAgentFileBinary.

type agentChunk struct {
	offset  int64
	payload []byte
}

type agentFileTask struct {
	events chan *protocol.Message // text control: ready/start/end/error
	chunks chan agentChunk        // binary download chunks
}

func (s *Server) registerAgentFileReq(requestID string) chan *protocol.Message {
	ch := make(chan *protocol.Message, 1)
	s.agentFileMu.Lock()
	s.agentFileReqs[requestID] = ch
	s.agentFileMu.Unlock()
	return ch
}

func (s *Server) removeAgentFileReq(requestID string) {
	s.agentFileMu.Lock()
	delete(s.agentFileReqs, requestID)
	s.agentFileMu.Unlock()
}

func (s *Server) registerAgentFileTask(taskID string) *agentFileTask {
	return s.registerBufferedFileTask(taskID, 64)
}

func (s *Server) registerBufferedFileTask(taskID string, chunks int) *agentFileTask {
	task := &agentFileTask{
		events: make(chan *protocol.Message, 8),
		chunks: make(chan agentChunk, chunks),
	}
	s.agentFileMu.Lock()
	s.agentFileTasks[taskID] = task
	s.agentFileMu.Unlock()
	return task
}

func (s *Server) removeAgentFileTask(taskID string) {
	s.agentFileMu.Lock()
	delete(s.agentFileTasks, taskID)
	s.agentFileMu.Unlock()
}

// routeAgentFileMsg consumes device text replies destined for an agent tool.
// Returns true when the message was claimed (caller must not process further).
func (s *Server) routeAgentFileMsg(msg *protocol.Message) bool {
	switch msg.Type {
	case protocol.MsgFileListResult:
		if msg.RequestID == "" {
			return false
		}
		s.agentFileMu.Lock()
		ch := s.agentFileReqs[msg.RequestID]
		s.agentFileMu.Unlock()
		if ch == nil {
			return false
		}
		select {
		case ch <- msg:
		default:
			log.Printf("agent file request %s result dropped (channel full)", msg.RequestID)
		}
		return true
	case protocol.MsgFileUploadReady, protocol.MsgFileDownloadStart, protocol.MsgFileTransferEnd, protocol.MsgFileTransferError:
		if msg.TaskID == "" {
			return false
		}
		s.agentFileMu.Lock()
		task := s.agentFileTasks[msg.TaskID]
		s.agentFileMu.Unlock()
		if task == nil {
			return false
		}
		select {
		case task.events <- msg:
		default:
			log.Printf("agent file task %s event dropped (type %s, channel full)", msg.TaskID, msg.Type)
		}
		return true
	}
	return false
}

// routeAgentFileBinary consumes device binary frames destined for an agent
// file task. Upload acks and binary end/cancel frames for agent tasks are
// drained and ignored; download chunks are forwarded to the task.
func (s *Server) routeAgentFileBinary(raw []byte) bool {
	typ, taskID, offset, payload, err := protocol.DecodeBinFrameOffset(raw)
	if err != nil {
		return false
	}
	switch typ {
	case protocol.BinFileDownloadChunk, protocol.BinFileUploadAck, protocol.BinFileTransferEnd, protocol.BinFileTransferCancel:
	default:
		return false
	}
	s.agentFileMu.Lock()
	task := s.agentFileTasks[taskID]
	s.agentFileMu.Unlock()
	if task == nil {
		return false
	}
	if typ == protocol.BinFileDownloadChunk {
		data := make([]byte, len(payload))
		copy(data, payload)
		select {
		case task.chunks <- agentChunk{offset: offset, payload: data}:
		default:
			log.Printf("agent file task %s chunk dropped (offset %d, channel full)", taskID, offset)
		}
	}
	return true
}

// newDeviceTools builds the tool set bound to one device connection and one
// thread working directory ("" = device default).
func newDeviceTools(srv *Server, client *ClientConn, cwd string) []agent.Tool {
	if client == nil {
		return nil
	}
	shell := &shellTool{srv: srv, client: client, Cwd: cwd}
	reader := &readFileTool{srv: srv, client: client, cwd: cwd}
	writer := &writeFileTool{srv: srv, client: client, cwd: cwd}
	return []agent.Tool{
		shell,
		&listDirTool{srv: srv, client: client},
		reader,
		writer,
		&editTool{reader: reader, writer: writer},
		&grepTool{shell: shell},
		&globTool{shell: shell},
		&todoWriteTool{},
		&webFetchTool{},
		&webSearchTool{},
	}
}
