package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxzan/gws"
	"rdev/internal/agent"
	"rdev/internal/protocol"
)

// Browser <-> server agent protocol (text JSON frames on /agent?device=<id>).
// In:  {op:"auth",password} | {op:"chat",text,model?,thread?,images?} | {op:"approve",id,allow,always?} |
//
//	{op:"steer",text} | {op:"promote_queue",thread?,queueId} | {op:"dequeue",thread?,queueId,id} (legacy: index) | {op:"new_thread"} | {op:"switch_thread",thread} |
//	{op:"delete_thread",thread} | {op:"rename_thread",thread,title} |
//	{op:"set_cwd",path} | {op:"set_permission_mode",mode} |
//	{op:"set_reasoning_level",level} | {op:"set_auto_approve",value} (legacy) |
//	{op:"get_changes"} | {op:"get_diff",path} |
//	{op:"ping"} | {op:"stop"} | {op:"reset"}
//
// Out: {op:"auth"|"auth_ok"|"auth_fail"|"delta"|"reasoning"|"tool_start"|"tool_end"|
//
//	"approval_request"|"todo"|"queue"|"turn_start"|"done"|"thread_list"|
//	"thread_switched"|"thread_meta"|"permission_mode"|"reasoning_level"|
//	"changes"|"diff"|"pong"|"stopped"|"error"|"reset_ok", ...}
//
// The agent session and any in-flight turn are owned by the server per
// device, not by a browser connection: events are broadcast to every
// attached socket and a reopened tab reattaches mid-run (auth_ok.running).
type agentMsg struct {
	Goal         *agentGoal         `json:"goal"`
	Loop         *agentLoop         `json:"loop"`
	Action       string             `json:"action,omitempty"`
	Objective    string             `json:"objective,omitempty"`
	TokenBudget  *int64             `json:"tokenBudget,omitempty"`
	Participants []agentParticipant `json:"participants,omitempty"`

	Automation         *automationView     `json:"automation,omitempty"`
	Pinned             bool                `json:"pinned,omitempty"`
	Offline            bool                `json:"offline,omitempty"`
	OfflineConfirmed   bool                `json:"offlineConfirmed,omitempty"`
	Type               string              `json:"type,omitempty"`
	Command            string              `json:"command,omitempty"`
	Success            *bool               `json:"success,omitempty"`
	Error              string              `json:"error,omitempty"`
	Code               string              `json:"code,omitempty"`
	Data               json.RawMessage     `json:"data,omitempty"`
	RPC                bool                `json:"-"`
	Display            json.RawMessage     `json:"display,omitempty"`
	ImagePreviews      []string            `json:"imagePreviews,omitempty"`
	Compacted          bool                `json:"compacted,omitempty"`
	Op                 string              `json:"op"`
	Password           string              `json:"password,omitempty"`
	Text               string              `json:"text,omitempty"`
	Device             string              `json:"device,omitempty"`
	Model              string              `json:"model,omitempty"`
	ModelName          string              `json:"modelName,omitempty"`
	Models             []map[string]any    `json:"models,omitempty"`
	Message            string              `json:"message,omitempty"`
	History            []agentHistoryEntry `json:"history,omitempty"`
	Running            bool                `json:"running,omitempty"`
	ID                 string              `json:"id,omitempty"`
	JobID              string              `json:"jobId,omitempty"`
	SubagentID         string              `json:"subagentId,omitempty"`
	CustomInstructions string              `json:"customInstructions,omitempty"`
	FromByte           *int64              `json:"fromByte,omitempty"`
	SessionFile        string              `json:"sessionFile,omitempty"`
	Name               string              `json:"name,omitempty"`
	Args               string              `json:"args,omitempty"`
	Result             string              `json:"result,omitempty"`
	IsError            bool                `json:"isError,omitempty"`
	Usage              *agentUsage         `json:"usage,omitempty"`
	Summary            string              `json:"summary,omitempty"`
	Diff               string              `json:"diff,omitempty"`
	Reasoning          string              `json:"reasoning,omitempty"`
	Thread             string              `json:"thread,omitempty"`
	Threads            []agentThreadInfo   `json:"threads,omitempty"`
	Todos              []todoItem          `json:"todos,omitempty"`
	QueueModes         []string            `json:"queueModes,omitempty"`
	QueueIDs           []string            `json:"queueIds,omitempty"`
	Queue              []string            `json:"queue,omitempty"`
	// Live carries the marshaled frames of the in-flight turn in auth_ok and
	// thread_switched so the browser replays mid-turn UI state; each element
	// is a complete tagged frame (turn_start/delta/tool_*/approval_request…).
	Live []json.RawMessage `json:"live,omitempty"`
	// Devices carries the cross-device thread list in all_threads frames:
	// every device that has agent threads, grouped, with its OS for icons.
	Devices []agentDeviceThreads `json:"devices,omitempty"`
	Items   json.RawMessage      `json:"items,omitempty"`
	Allow   bool                 `json:"allow,omitempty"`
	Always  bool                 `json:"always,omitempty"`
	// Options carries the ask_request option list ([{label,description}]);
	// Answer is the browser's ask_response payload (selected label or free
	// text, "" = dismissed).
	Options     json.RawMessage `json:"options,omitempty"`
	Answer      string          `json:"answer,omitempty"`
	Value       bool            `json:"value,omitempty"`
	Files       []string        `json:"files,omitempty"`
	AutoApprove bool            `json:"autoApprove,omitempty"`
	// Mode is the permission mode: "full" | "write" | "ask"
	// (set_permission_mode in, permission_mode out).
	Mode string `json:"mode,omitempty"`
	// Level is the reasoning effort: "off" | "low" | "medium" | "high"
	// (set_reasoning_level in, reasoning_level out).
	Level string `json:"level,omitempty"`
	// PermissionMode and ReasoningLevel carry the current settings in
	// auth_ok. AutoApprove is kept for legacy browsers (= mode == "full").
	PermissionMode string `json:"permissionMode,omitempty"`
	ReasoningLevel string `json:"reasoningLevel,omitempty"`
	// Images is raw because the shapes differ by direction: chat input
	// carries [{name,mime,data}] objects; turn_start output carries name
	// strings.
	Images  json.RawMessage `json:"images,omitempty"`
	Working *agentWorking   `json:"working,omitempty"`
	// Index is the dequeue payload: position of the queued follow-up to drop.
	Index            int      `json:"index,omitempty"`
	QueueID          string   `json:"queueId,omitempty"`
	EditToken        string   `json:"editToken,omitempty"`
	ExpectedText     *string  `json:"expectedText,omitempty"`
	ExpectedQueueIDs []string `json:"expectedQueueIds,omitempty"`
	BeforeID         string   `json:"beforeId,omitempty"`
	Restore          bool     `json:"restore,omitempty"`
	Removed          bool     `json:"removed,omitempty"`
	// Canceled marks stopped frames: true when a running turn was canceled,
	// false for the idle-stop ack.
	Canceled bool `json:"canceled,omitempty"`
	// Cwd and Branch describe a thread's working directory and git branch:
	// auth_ok (current thread) and thread_meta broadcasts. Path is the
	// set_cwd input ("" = restore device default); Title is the
	// rename_thread input.
	Cwd    string `json:"cwd,omitempty"`
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path,omitempty"`
	Title  string `json:"title,omitempty"`
	// Content and Truncated carry the file_read reply (op "file_content");
	// Message holds the error text when the read failed. Encoding/Mime mark
	// image replies: Content is base64 and the browser renders <img>.
	Content    string `json:"content,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Encoding   string `json:"encoding,omitempty"`
	Mime       string `json:"mime,omitempty"`
	PreviewURL string `json:"previewUrl,omitempty"`
	// Parent, Entries and Roots carry the dir_list reply (op "dir_entries")
	// for the create-project path browser; Message holds the error text.
	Parent  string          `json:"parent,omitempty"`
	Entries []agentDirEntry `json:"entries,omitempty"`
	Roots   []string        `json:"roots,omitempty"`
	// Q is the search_files query (in and echoed back in file_search).
	Q string `json:"q,omitempty"`
}

// agentDirEntry is one child of a listed directory on a device; Dir marks
// subdirectories (files are shown dimmed and are not navigable).
type agentDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

// agentChangeFile is one changed file of a thread's git working tree; Status
// is the porcelain letter ("M", "A", "D", "R", "C", "U") or "??" for
// untracked files. Adds/Dels are the numstat line counts (0 for untracked
// files and binary diffs, which numstat reports as "-").
type agentChangeFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Adds   int    `json:"adds"`
	Dels   int    `json:"dels"`
}

// agentChangesMsg is the get_changes reply, also pushed proactively after
// thread meta probes (turn done, set_cwd, switch_thread, auth_ok). Error is
// non-empty when the device is offline or cwd is not a git repository.
type agentChangesMsg struct {
	Op     string            `json:"op"`
	ID     string            `json:"id,omitempty"`
	Thread string            `json:"thread,omitempty"`
	Cwd    string            `json:"cwd"`
	Branch string            `json:"branch"`
	Files  []agentChangeFile `json:"files"`
	Error  string            `json:"error"`
}

// agentDiffMsg is the get_diff reply: the unified diff of one path, or an
// error ("untracked" for files git does not track).
type agentDiffMsg struct {
	Op     string `json:"op"`
	ID     string `json:"id,omitempty"`
	Thread string `json:"thread,omitempty"`
	Path   string `json:"path"`
	Diff   string `json:"diff"`
	Error  string `json:"error"`
}

// agentImage is one image attachment of a chat message.
type agentImage struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Data string `json:"data"` // base64
}

// Image attachment limits: at most agentMaxImages per chat message, each at
// most agentMaxImageBytes after base64 decoding.
const (
	agentMaxImages     = 4
	agentMaxImageBytes = 4 << 20 // 4 MiB
)

// parseAgentImages validates and decodes the images field of a chat message.
func parseAgentImages(raw json.RawMessage) ([]agent.MessageImage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var in []agentImage
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("invalid images: %v", err)
	}
	if len(in) > agentMaxImages {
		return nil, fmt.Errorf("too many images: %d, max %d", len(in), agentMaxImages)
	}
	out := make([]agent.MessageImage, 0, len(in))
	for _, img := range in {
		decoded, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			return nil, fmt.Errorf("image %q: invalid base64 data", img.Name)
		}
		if len(decoded) > agentMaxImageBytes {
			return nil, fmt.Errorf("image %q is %d bytes, max %d", img.Name, len(decoded), agentMaxImageBytes)
		}
		out = append(out, agent.MessageImage{Name: img.Name, Mime: img.Mime, Data: img.Data, Preview: agentImagePreview(decoded)})
	}
	return out, nil
}

// agentImageNames returns attachment labels for live and replayed user input;
// image previews travel separately from the original provider payload.
func agentImageNames(images []agent.MessageImage) []string {
	if len(images) == 0 {
		return nil
	}
	names := make([]string, len(images))
	for i, img := range images {
		names[i] = img.Name
	}
	return names
}

type agentUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// agentWorking reports the in-flight turn's start time and current tool so a
// reattached browser can restore its working indicator.
type agentWorking struct {
	Since string `json:"since"`
	Tool  string `json:"tool,omitempty"`
}

// agentThreadInfo is one thread in a thread_list / auth_ok broadcast.
type agentThreadInfo struct {
	PinnedAt       int64  `json:"pinnedAt,omitempty"`
	OutcomeAt      string `json:"outcomeAt,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	BackgroundJobs int    `json:"backgroundJobs,omitempty"`
	ID             string `json:"id"`
	Title          string `json:"title"`
	UpdatedAt      string `json:"updatedAt"`
	Running        bool   `json:"running"`
	Waiting        string `json:"waiting,omitempty"`
	// Model is the thread's canonical "provider/model".
	Model string `json:"model,omitempty"`
	// Tool is the currently executing tool name; only set while running.
	Tool string `json:"tool,omitempty"`
}

// approvalDecision is the browser's answer to an approval_request.
type approvalDecision struct {
	allow  bool
	always bool
}

// agentApprovalTimeout bounds how long a dangerous tool call waits for the
// browser's decision; on expiry the call is denied.
const agentApprovalTimeout = 5 * time.Minute

// agentQueuedChat is one chat message waiting behind a running turn.
type agentQueuedChat struct {
	editOwner *gws.Conn
	editToken string
	editUntil time.Time
	id        string
	steering  bool // inject at the next settled model/tool boundary
	display   json.RawMessage
	text      string
	images    []agent.MessageImage
	model     string // requested model ("" = keep the thread's)
}

// takeQueuedChat releases the removed slot, including original image data.
// Caller holds agentSessionsMu and supplies an in-range index.
func takeQueuedChat(queue *[]agentQueuedChat, index int) agentQueuedChat {
	items := *queue
	item := items[index]
	if index == 0 {
		items[0] = agentQueuedChat{}
		*queue = items[1:]
	} else {
		copy(items[index:], items[index+1:])
		items[len(items)-1] = agentQueuedChat{}
		*queue = items[:len(items)-1]
	}
	return item
}

// queueIDs assigns identity once under agentSessionsMu, including entries
// created by background notifications. Identity survives queue reordering.
func queueIDs(q []agentQueuedChat) []string {
	ids := make([]string, len(q))
	for i := range q {
		if q[i].id == "" {
			q[i].id = rand.Text()
		}
		ids[i] = q[i].id
	}
	return ids
}

// queueTexts reduces a pending queue to its texts for the queue broadcast.
func queueTexts(q []agentQueuedChat) []string {
	texts := make([]string, len(q))
	for i, c := range q {
		texts[i] = c.text
		var display struct {
			Job *agentJobSnapshot `json:"job"`
		}
		if json.Unmarshal(c.display, &display) == nil && display.Job != nil {
			texts[i] = fmt.Sprintf("Background task: %s (%s)", display.Job.Label, display.Job.State)
		}
	}
	return texts
}

// agentThread is one conversation thread of a device: its own LLM session,
// history, todo list, message queue and (at most one) in-flight turn.
// Guarded by Server.agentSessionsMu.
type agentThread struct {
	scheduled        *scheduledAgentTurn
	goal             *agentGoal
	loop             *agentLoop
	goalActivity     string
	autonomyRevision uint64
	autonomyPending  bool
	autonomyCancel   context.CancelFunc
	turnAutonomy     string
	deviceScope      *agentDeviceScope

	automation       *agentAutomation
	pinnedAt         int64
	pendingReasoning bool // rebuild at the next turn boundary after an effort change
	turnDisplay      json.RawMessage
	turnCommand      bool // compact/tool commands do not consume model-boundary steering
	// Acquire before agentSessionsMu; disk I/O never holds the global lock.
	persistMu     sync.Mutex
	jobs          *agentJobs
	id            string
	session       *agent.Session
	sessionClient *ClientConn // device connection session's tools are bound to
	model         string      // this thread's canonical "provider/model"
	running       bool        // a chat turn is in flight
	cancel        context.CancelFunc
	turnCtx       context.Context
	steeringWake  chan struct{} // guarded by agentSessionsMu; wakes only passive job waits
	// Freeze the pre-turn transcript: semantic compaction may replace session
	// history while live frames still describe the original running turn.
	turnHistory []agent.Message
	turnHistIdx int
	// epoch is bumped by reset; a turn that started before the bump skips
	// its save and terminal broadcast (its history belongs to the pre-reset
	// conversation).
	epoch        int
	queue        []agentQueuedChat // chat messages received while running
	todos        []todoItem        // latest todo_write list
	todoPhases   []rpcTodoPhase    // OMP phase-shaped view, when set through RPC
	alwaysAllow  map[string]bool   // tool names approved "always" in this thread
	aprSeq       int               // approval/question id counter
	approvals    map[string]chan approvalDecision
	questions    map[string]chan string // pending ask tool questions (id -> answer)
	toolPaths    map[string]string      // in-flight tool call id -> path arg (edit/write_file)
	turnFiles    []string               // files changed by edit/write_file this turn
	workingSince time.Time              // current turn start
	outcomeAt    string
	lastOutcome  string // latest foreground turn: done, error, stopped
	workingTool  string // currently executing tool name
	updatedAt    time.Time
	// lastUsage is the token accounting of the most recent completed turn,
	// replayed in auth_ok/thread_switched for the context-usage indicator.
	lastUsage    *agentUsage
	cwd          string // working directory for tools ("" = device default)
	branch       string // last probed git branch of cwd ("" = unknown/not a repo)
	metaProbeSeq uint64 // rejects stale Git probe results after cwd changes
	title        string // explicit thread title ("" = derive from first user message)
	// live holds the marshaled frames of the in-flight turn (turn_start …
	// terminal frame) so a browser switching in mid-turn can replay the
	// exact UI state; cleared at the next turn start, read only while
	// running (idle attach replays full history instead).
	live agentReplayBuffer
}

// Permission modes: how much of the agent's tool use needs browser
// confirmation.
const (
	agentPermFull  = "full"  // auto-approve everything, never ask
	agentPermWrite = "write" // default: only dangerous tools ask
	agentPermAsk   = "ask"   // every tool call asks, including read-only
)

// agentPermNeedsApproval reports whether a tool call must be confirmed by
// the browser under the given permission mode (before always-allow memory).
func agentPermNeedsApproval(mode string, dangerous bool) bool {
	switch mode {
	case agentPermFull:
		return false
	case agentPermAsk:
		return true
	default: // write
		return dangerous
	}
}

// validPermMode reports whether mode is a known permission mode.
func validPermMode(mode string) bool {
	return mode == agentPermFull || mode == agentPermWrite || mode == agentPermAsk
}

// validReasoningLevel reports whether level is a known reasoning level.
func validReasoningLevel(level string) bool {
	switch level {
	case "off", "low", "medium", "high":
		return true
	}
	return false
}

// agentSessionEntry is the server-owned per-device agent state: threads (LLM
// sessions) and in-flight turns live here, decoupled from browser WS
// lifetimes. Browsers attach as subscribers; events are broadcast to all.
// Guarded by Server.agentSessionsMu.
type agentSessionEntry struct {
	prefsMu         sync.Mutex // serializes disk updates for this device, outside agentSessionsMu
	deviceID        string
	client          *ClientConn // latest known live device connection
	os              string      // device OS (client registration or desktop platform)
	model           string      // canonical "provider/model"
	permMode        string      // agentPerm*; default agentPermWrite
	reasoningLevel  string      // off|low|medium|high; default "medium"
	subs            map[*gws.Conn]bool
	lastActive      time.Time
	threads         map[string]*agentThread
	current         string        // current thread id
	defaultCwd      string        // device default working directory
	defaultCwdProbe chan struct{} // coalesces discovery across concurrent panel requests
}

// agentConn is the per-connection (browser tab) state stored in
// socket.Session(); the shared session/turn state lives in entry.
// current is THIS connection's selected thread: browsers are independent
// (one browser switching threads must not yank another browser's view);
// entry.current only seeds new connections and persists as the resume
// default.
type agentConn struct {
	// Optional synchronous reply sink for in-process management tools.
	reply func(agentMsg)
	// Management views share the original connection write lock.
	responseTo     *agentConn
	responseThread string
	responseID     string
	socket         *gws.Conn
	deviceID       string
	client         *ClientConn
	authed         bool
	entry          *agentSessionEntry
	current        string

	writeMu sync.Mutex
}

// goAgent tracks server-owned asynchronous agent work so test and shutdown
// callers can wait until persistence and device probes have stopped.
func (s *Server) goAgent(fn func()) {
	s.agentWG.Add(1)
	go func() {
		defer s.agentWG.Done()
		fn()
	}()
}

// waitAgentWork is intentionally small and safe for tests; production callers
// may use it while draining the server.
func (s *Server) waitAgentWork() { s.agentWG.Wait() }

// writeData sends one pre-marshaled frame; reports whether the write worked
// so broadcasts can prune dead subscribers.
func (ac *agentConn) writeData(data []byte) bool {
	ac.writeMu.Lock()
	defer ac.writeMu.Unlock()
	_ = ac.socket.SetWriteDeadline(time.Now().Add(wsWriteWait))
	defer ac.socket.SetWriteDeadline(time.Time{})
	if err := ac.socket.WriteMessage(gws.OpcodeText, data); err != nil {
		log.Printf("agent write failed: device=%s err=%v", ac.deviceID, err)
		return false
	}
	return true
}

func (ac *agentConn) writeJSON(msg agentMsg) {
	if ac.reply != nil {
		ac.reply(msg)
		return
	}
	if ac.responseTo != nil {
		if msg.Op == "error" {
			msg.Op = "thread_action_error"
			msg.Thread = ac.responseThread
		}
		msg.Device = ac.deviceID
		msg.ID = ac.responseID
		ac.responseTo.writeJSON(msg)
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	ac.writeData(data)
}

// sendError reports an unattributable failure (auth, device misconfig):
// the frame carries no thread tag and only lands on the page status.
func (ac *agentConn) sendError(message string) {
	ac.writeJSON(agentMsg{Op: "error", Message: message})
}

// sendErrorThread reports a failure attributable to one thread: the tagged
// frame routes to that thread's transcript sink.
func (ac *agentConn) sendErrorThread(thread, message string) {
	ac.writeJSON(agentMsg{Op: "error", Thread: thread, Message: message})
}

// writeChanges sends a get_changes reply to this socket.
func (ac *agentConn) writeChanges(msg agentChangesMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	ac.writeData(data)
}

// writeDiff sends a get_diff reply to this socket.
func (ac *agentConn) writeDiff(msg agentDiffMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	ac.writeData(data)
}

type agentWSHandler struct {
	gws.BuiltinEventHandler
	srv         *Server
	adminBypass bool // valid admin token: skip device password
}

func (h *agentWSHandler) OnOpen(socket *gws.Conn) {
	deviceIDI, _ := socket.Session().Load("deviceID")
	if deviceIDI == nil {
		socket.WriteClose(1000, []byte("missing device parameter"))
		return
	}
	deviceID := deviceIDI.(string)

	h.srv.mu.RLock()
	client, ok := h.srv.clients[deviceID]
	h.srv.mu.RUnlock()
	if deviceID == agentServerDeviceID {
		client = &ClientConn{ID: agentServerDeviceID}
		ok = true
	}
	if !ok && !h.adminBypass {
		// Offline history needs admin authorization: device passwords cannot be checked without a live device.
		payload, _ := json.Marshal(agentMsg{Op: "error", Code: "offline_admin_required", Message: "Offline history requires a server administrator token; the device password cannot be verified until the device reconnects."})
		_ = socket.WriteMessage(gws.OpcodeText, payload)
		socket.WriteClose(1013, []byte("device is temporarily offline"))
		return
	}

	ac := &agentConn{socket: socket, deviceID: deviceID, client: client}
	socket.Session().Store("agentConn", ac)

	if h.srv.Agent == nil || !h.srv.Agent.Enabled() {
		ac.sendError("agent not configured: set --agent-api-key / RDEV_AGENT_API_KEY")
		socket.WriteClose(1000, nil)
		return
	}

	if client != nil && client.Password != "" && !h.adminBypass {
		ac.writeJSON(agentMsg{Op: "auth", Message: "Device '" + deviceID + "' requires password"})
		return
	}

	ac.authed = true
	h.establish(ac, client)
}

// buildAgentSession creates the LLM session for one provider/model pair,
// bound to the device tools and the thread's approval flow.
func (s *Server) buildAgentSession(entry *agentSessionEntry, th *agentThread, client *ClientConn, provider *AgentProvider, model *AgentModelConfig) *agent.Session {
	if th.jobs == nil {
		jobs := newAgentJobs(nil)
		jobs.persistenceEnabled = s.DataDir != "" && s.AgentSessionTTL > 0
		updates := &agentJobUpdates{launch: s.goAgent, publish: func() {
			if s.broadcastAgentJobsChanged(entry, th, jobs) {
				s.broadcastThreadList(entry)
			}
		}}
		checkpoints := &agentJobUpdates{launch: s.goAgent, publish: func() { s.persistAgentJobs(entry, th, jobs) }}
		jobs.onChange = func() { checkpoints.Notify(); updates.Notify() }
		jobs.onComplete = func(job agentJobSnapshot) { s.deliverAgentJob(entry, th, jobs, job) }
		th.jobs = jobs
	}
	if entry.deviceID != agentServerDeviceID && th.deviceScope == nil {
		th.deviceScope = newAgentDeviceScope(s, entry.deviceID, th.cwd)
	}
	tools := s.agentBaseTools(entry, client, th.cwd)
	if th.deviceScope != nil {
		tools = append(tools, &connectedDevicesTool{srv: s, scope: th.deviceScope}, &remoteDeviceTool{srv: s, scope: th.deviceScope})
	}
	if entry.deviceID == agentServerDeviceID {
		tools = append(tools, &agentSessionsTool{srv: s, owner: entry, thread: th.id}, &agentSessionsTool{srv: s, owner: entry, thread: th.id, mutate: true})
	}
	tools = append(tools, s.automationTools(entry, th)...)
	if entry.deviceID != agentServerDeviceID && client != nil {
		tools = append(tools, &backgroundTool{jobs: th.jobs, scope: th.deviceScope, shell: &shellTool{srv: s, client: client, Cwd: th.cwd}})
	} else if entry.deviceID == agentServerDeviceID {
		tools = append(tools, &backgroundTool{jobs: th.jobs, resolveShell: func(device string) (*shellTool, error) {
			tool, err := s.resolveDeviceTool(device, "shell")
			if err != nil {
				return nil, err
			}
			return tool.(*shellTool), nil
		}})
	}
	tools = append(tools, &jobsTool{jobs: th.jobs, steering: func() <-chan struct{} { return s.agentSteeringSignal(th) }}, &canvasTool{})
	tools = append(tools, &askTool{ask: func(_ context.Context, question string, options []askOption) string {
		return s.requestQuestion(entry, th, question, marshalAskOptions(options))
	}})
	tools = append(tools, s.scheduleAgentTools(entry, th)...)
	cfg := agent.Config{
		API:             provider.modelAPI(model),
		ProviderID:      provider.ID,
		MaxOutputTokens: model.MaxTokens,
		Thinking:        model.Thinking,
		BaseURL:         provider.BaseURL,
		APIKey:          provider.APIKey,
		Model:           model.ID,
		SystemPrompt:    buildAgentSystemPrompt(entry.deviceID, agentEffectiveCwdLocked(entry, th)),
		Tools:           tools,
		MaxTurns:        s.AgentMaxTurns,
		ReasoningEffort: entry.reasoningLevel,
		Approve: func(tc agent.ToolCall, args json.RawMessage) (bool, error) {
			return s.requestApproval(entry, th, tc, args)
		},
		// Read at call time so set_permission_mode applies to existing
		// sessions without a rebuild.
		ApproveAll: func() bool {
			s.agentSessionsMu.Lock()
			defer s.agentSessionsMu.Unlock()
			return agentPermNeedsApproval(entry.permMode, false)
		},
	}
	if th.deviceScope != nil {
		cfg.SystemPrompt += "\nThis conversation supports joined devices. Use list_devices to see authorized participants and device_tool with an explicit device ID for cross-device work. Native tools default to the original device. Each participant has its own working directory. Never assume a path or operating system from another device. Subagents may target an explicitly joined device using device. Only the user can add participants."
	}
	cwd := th.cwd
	cfg.Tools = append(cfg.Tools, &subagentTool{
		jobs: th.jobs, config: cfg,
		tools: func() []agent.Tool {
			if client == nil && entry.deviceID != agentServerDeviceID {
				return nil
			}
			return s.agentBaseTools(entry, client, cwd)
		},
		deviceContext: func(ctx context.Context, device string) (context.Context, func(), error) {
			if th.deviceScope == nil {
				return nil, nil, fmt.Errorf("device targeting unavailable")
			}
			return th.deviceScope.begin(ctx, device)
		},
		deviceTools: func(device string) ([]agent.Tool, error) {
			if th.deviceScope == nil {
				return nil, fmt.Errorf("use server device_tool for remote routing")
			}
			return th.deviceScope.tools(device)
		},
	})
	if entry.deviceID != agentServerDeviceID && client == nil {
		availableTools := cfg.Tools
		cfg.Tools = nil
		cfg.SystemPrompt = "The original device is offline. Historical observations may be outdated."
		if th.deviceScope != nil && len(th.deviceScope.snapshot()) > 1 {
			for _, tool := range availableTools {
				switch tool.Name() {
				case "list_devices", "device_tool", "computer", "cua", "skill", "browser", "autojs", "jobs", "subagent", "todo_write", "web_fetch", "web_search":
					cfg.Tools = append(cfg.Tools, tool)
				}
			}
			cfg.Tools = append(cfg.Tools, &backgroundTool{jobs: th.jobs, scope: th.deviceScope})
			cfg.SystemPrompt += " Use list_devices and explicitly target an authorized online participant with device_tool. Never execute on the offline original device."
		} else {
			cfg.SystemPrompt += " Discuss existing context only; no tools are available."
		}
	}
	if len(cfg.Tools) != 0 {
		availableTools := append([]agent.Tool(nil), cfg.Tools...)
		cfg.Tools = append(cfg.Tools, &agentJSTool{tools: availableTools})
	}
	if th.goal != nil && th.goal.Status == "active" {
		goal, epoch := th.goal, th.epoch
		cfg.Tools = append(cfg.Tools, &goalTool{srv: s, entry: entry, thread: th, goal: goal, epoch: epoch})
		cfg.SystemPrompt += "\nGoal mode is explicitly enabled. Objective: " + goal.Objective + ". Continue making concrete progress; use goal complete only after achieving it. Do not claim completion while blocked."
		cfg.OnUsage = func(usage agent.Usage) { s.accountGoalUsage(entry, th, goal, epoch, usage) }
	}
	if th.scheduled != nil {
		s.configureScheduledAgent(entry, th, &cfg)
	}
	th.pendingReasoning = false
	return agent.New(cfg)
}

// newAgentThreadLocked creates a thread (new session for entry's model) with
// the given history, working directory and explicit title. Caller must hold
// agentSessionsMu.
func (s *Server) newAgentThreadLocked(entry *agentSessionEntry, threadID string, history []agent.Message, cwd, title string) *agentThread {
	provider, model, canonical, err := s.Agent.Resolve(entry.model)
	if err != nil {
		provider, model, canonical, _ = s.Agent.Resolve("")
	}
	th := &agentThread{
		id:            threadID,
		model:         canonical,
		sessionClient: entry.client,
		alwaysAllow:   make(map[string]bool),
		approvals:     make(map[string]chan approvalDecision),
		questions:     make(map[string]chan string),
		toolPaths:     make(map[string]string),
		updatedAt:     time.Now(),
		cwd:           cwd,
		title:         title,
	}
	th.session = s.buildAgentSession(entry, th, entry.client, provider, model)
	if len(history) > 0 {
		th.session.SetHistory(history)
	}
	return th
}

// agentSessionFor returns the per-device agent entry, creating it on first
// use: persisted threads are restored from JSONL history (migrating the
// legacy single-file layout to t_1) and the most recently updated thread
// becomes current (cold start; after that the in-memory entry is the source
// of truth).
func (s *Server) agentSessionFor(deviceID string, client *ClientConn) (*agentSessionEntry, error) {
	s.agentEvictOnce.Do(func() { go s.agentEvictionLoop() })
	s.agentSessionsMu.Lock()
	defer s.agentSessionsMu.Unlock()
	if s.agentSessions == nil {
		s.agentSessions = make(map[string]*agentSessionEntry)
	}
	if entry, ok := s.agentSessions[deviceID]; ok {
		return entry, nil
	}
	_, _, canonical, err := s.Agent.Resolve("")
	if err != nil {
		return nil, err
	}
	entry := &agentSessionEntry{
		deviceID:       deviceID,
		client:         client,
		model:          canonical,
		permMode:       agentPermWrite,
		reasoningLevel: "medium",
		subs:           make(map[*gws.Conn]bool),
		lastActive:     time.Now(),
		threads:        make(map[string]*agentThread),
	}
	prefs := s.loadAgentPrefs(deviceID)
	if validPermMode(prefs.PermissionMode) {
		entry.permMode = prefs.PermissionMode
	}
	if validReasoningLevel(prefs.ReasoningLevel) {
		entry.reasoningLevel = prefs.ReasoningLevel
	}
	entry.os = prefs.OS
	if client != nil {
		if os := clientOS(client); os != "" {
			entry.os = os
		}
	}
	recentThread, recentModel := "", ""
	if store := s.agentStore(); store != nil {
		store.migrate(deviceID)
		for _, meta := range store.listThreads(deviceID) {
			msgs, savedModel, cwd, title := store.load(deviceID, meta.ID)
			th := s.newAgentThreadLocked(entry, meta.ID, msgs, cwd, title)
			th.pinnedAt = prefs.Pinned[meta.ID]
			th.goal, th.loop = store.loadAutonomy(deviceID, meta.ID)
			if th.deviceScope != nil {
				for _, member := range prefs.Participants[meta.ID] {
					if member.Device != "" && member.Device != agentServerDeviceID && member.Device != deviceID {
						th.deviceScope.members[member.Device] = member.Cwd
					}
				}
			}
			th.updatedAt = meta.UpdatedAt
			th.lastOutcome = restoredAgentOutcome(meta.Outcome)
			th.outcomeAt = meta.OutcomeAt
			// Per-thread model: a thread restored with a different model
			// keeps it instead of being retargeted to the entry default.
			if savedModel != "" && savedModel != th.model {
				if provider, model, canonical, rerr := s.Agent.Resolve(savedModel); rerr == nil && canonical != th.model {
					newSession := s.buildAgentSession(entry, th, entry.client, provider, model)
					newSession.SetHistory(th.session.History())
					th.session = newSession
					th.model = canonical
				}
			}
			entry.threads[meta.ID] = th
			if th.jobs.restore(store.loadJobs(deviceID, meta.ID)) {
				// Rewrite interrupted states once after initialization, outside
				// the session lock, without re-delivering completion messages.
				owner := th.jobs
				s.goAgent(func() { s.persistAgentJobs(entry, th, owner) })
			}
			// The most recently updated thread is listed first.
			if recentThread == "" {
				recentThread, recentModel = meta.ID, savedModel
			}
		}
	}
	// Model priority: persisted prefs (when resolvable) > most recent
	// thread's meta model > server default.
	for _, cand := range []string{prefs.Model, recentModel} {
		if cand == "" {
			continue
		}
		if _, _, c, rerr := s.Agent.Resolve(cand); rerr == nil {
			entry.model = c
			break
		}
	}
	// Current thread: the persisted one when it still exists, else the most
	// recently updated thread.
	if prefs.CurrentThread != "" {
		if _, ok := entry.threads[prefs.CurrentThread]; ok {
			entry.current = prefs.CurrentThread
		}
	}
	if entry.current == "" {
		entry.current = recentThread
	}
	if entry.current == "" {
		th := s.newAgentThreadLocked(entry, newAgentThreadID(), nil, "", "")
		entry.threads[th.id] = th
		entry.current = th.id
	}
	s.agentSessions[deviceID] = entry
	// Probe the device's default working directory once (off-lock); the
	// result backfills thread_meta for the current thread.
	if client != nil && deviceID != agentServerDeviceID {
		s.goAgent(func() { s.probeAgentDefaultCwd(entry) })
	}
	return entry, nil
}

// newAgentThreadID mints a thread id from the current time.
func newAgentThreadID() string {
	return fmt.Sprintf("t_%d", time.Now().UnixNano())
}

// rebindAgentSessionLocked rebuilds idle threads' sessions against the
// latest live device connection (after a device reconnect), keeping model
// and history. Running threads are skipped — rebinding mid-run would strand
// the running turn's history tail; they rebind when the next turn starts.
func (s *Server) rebindAgentSessionLocked(entry *agentSessionEntry) {
	for _, th := range entry.threads {
		if th.sessionClient == entry.client || th.running {
			continue
		}
		provider, model, err := s.resolveAgentModel(th.model)
		if err != nil {
			return
		}
		newSession := s.buildAgentSession(entry, th, entry.client, provider, model)
		newSession.SetHistory(th.session.History())
		th.session = newSession
		th.sessionClient = entry.client
	}
}

// resolveAgentModel resolves a "provider/model" reference, falling back to
// the configured default when the reference is empty or unknown.
func (s *Server) resolveAgentModel(modelRef string) (*AgentProvider, *AgentModelConfig, error) {
	provider, model, _, err := s.Agent.Resolve(modelRef)
	if err != nil && modelRef != "" {
		provider, model, _, err = s.Agent.Resolve("")
	}
	return provider, model, err
}

// agentBroadcast sends msg to every socket attached to entry, pruning
// subscribers whose write fails.
func (s *Server) agentBroadcast(entry *agentSessionEntry, msg agentMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	s.agentBroadcastData(entry, data)
}

// agentBroadcastTurn broadcasts one frame of a thread's in-flight turn. The
// frame is tagged with the thread id (browsers viewing another thread drop
// it) and appended to th.live so a browser switching in mid-turn replays the
// exact UI state. Lock must NOT be held.
func (s *Server) agentBroadcastTurn(entry *agentSessionEntry, th *agentThread, msg agentMsg) {
	msg.Thread = th.id
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("agent marshal error: %v", err)
		return
	}
	s.agentSessionsMu.Lock()
	th.live.Append(msg, data)
	s.agentSessionsMu.Unlock()
	s.agentBroadcastData(entry, data)
	if msg.Op == "turn_start" {
		// Cross-device snapshots include disk history; never delay turn admission.
		s.goAgent(func() { s.broadcastThreadList(entry) })
	}
}

// agentBroadcastData broadcasts a pre-marshaled frame; used by the
// changes/diff replies which are not agentMsg-shaped.
func (s *Server) agentBroadcastData(entry *agentSessionEntry, data []byte) {
	s.agentSessionsMu.Lock()
	subs := make([]*gws.Conn, 0, len(entry.subs))
	for c := range entry.subs {
		subs = append(subs, c)
	}
	s.agentSessionsMu.Unlock()

	var dead []*gws.Conn
	for _, c := range subs {
		acRaw, _ := c.Session().Load("agentConn")
		if acRaw == nil {
			dead = append(dead, c)
			continue
		}
		if !acRaw.(*agentConn).writeData(data) {
			dead = append(dead, c)
		}
	}
	if len(dead) > 0 {
		s.agentSessionsMu.Lock()
		for _, c := range dead {
			delete(entry.subs, c)
		}
		s.agentSessionsMu.Unlock()
	}
}

// agentEvictionLoop hourly drops idle entries (no running turn, no attached
// browser); the next connect cold-starts again from the JSONL history.
func (s *Server) agentEvictionLoop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		s.agentSessionsMu.Lock()
		for id, entry := range s.agentSessions {
			busy := false
			for _, th := range entry.threads {
				if th.running || th.autonomyPending || th.turnCtx != nil || len(th.queue) > 0 || (th.jobs != nil && (th.jobs.Running() || th.jobs.pendingPersistence())) {
					busy = true
					break
				}
			}
			// Recently active entries stay: a browser may reconnect and
			// expect the warm session (30 min grace).
			if busy || len(entry.subs) > 0 || time.Since(entry.lastActive) < 30*time.Minute {
				continue
			}
			for _, th := range entry.threads {
				if th.jobs != nil {
					th.jobs.Close()
					th.jobs = nil
				}
			}
			delete(s.agentSessions, id)
		}
		s.agentSessionsMu.Unlock()
	}
}

// agentThreadListLocked snapshots entry's threads, most recently updated
// first. Caller must hold agentSessionsMu.
func agentThreadListLocked(entry *agentSessionEntry) []agentThreadInfo {
	list := make([]agentThreadInfo, 0, len(entry.threads))
	for _, th := range entry.threads {
		title := th.title
		if title == "" {
			title = threadTitle(th.session.History())
		}
		info := agentThreadInfo{
			ID:        th.id,
			PinnedAt:  th.pinnedAt,
			Title:     title,
			UpdatedAt: th.updatedAt.UTC().Format(time.RFC3339),
			Running:   th.running,
			Outcome:   th.lastOutcome,
			OutcomeAt: th.outcomeAt,
			Model:     th.model,
		}
		if th.jobs != nil {
			info.BackgroundJobs = th.jobs.RunningCount()
		}
		if th.running {
			info.Tool = th.workingTool
			if len(th.approvals) > 0 {
				info.Waiting = "approval"
			} else if len(th.questions) > 0 {
				info.Waiting = "answer"
			}
		}
		list = append(list, info)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].UpdatedAt > list[j].UpdatedAt })
	return list
}

// agentDeviceThreads is one device's slice of the cross-device thread list.
type agentDeviceThreads struct {
	Device  string            `json:"device"`
	OS      string            `json:"os,omitempty"`
	Online  bool              `json:"online"`
	Threads []agentThreadInfo `json:"threads"`
}

// collectAllThreads builds the cross-device thread list: live entries come
// from memory (running flags included); devices that only exist on disk are
// listed from the session store with running=false. Directory names are the
// device ids (sanitizeAgentHistoryName is identity for realistic ids).
func (s *Server) collectAllThreads() []agentDeviceThreads {
	s.agentSessionsMu.Lock()
	live := make(map[string]agentDeviceThreads, len(s.agentSessions))
	for id, entry := range s.agentSessions {
		live[sanitizeAgentHistoryName(id)] = agentDeviceThreads{
			Device:  id,
			OS:      entry.os,
			Online:  entry.client != nil,
			Threads: agentThreadListLocked(entry),
		}
	}
	s.agentSessionsMu.Unlock()

	store := s.agentStore()
	if store == nil {
		out := make([]agentDeviceThreads, 0, len(live))
		for _, d := range live {
			out = append(out, d)
		}
		return out
	}
	out := make([]agentDeviceThreads, 0, len(live)+2)
	seen := make(map[string]bool, len(live))
	for _, d := range live {
		out = append(out, d)
		seen[sanitizeAgentHistoryName(d.Device)] = true
	}
	for _, dirID := range store.listDevices() {
		if seen[dirID] {
			continue
		}
		prefs := s.loadAgentPrefs(dirID)
		os := prefs.OS
		metas := store.listThreads(dirID)
		threads := make([]agentThreadInfo, 0, len(metas))
		for _, m := range metas {
			threads = append(threads, agentThreadInfo{
				ID:        m.ID,
				PinnedAt:  prefs.Pinned[m.ID],
				Outcome:   restoredAgentOutcome(m.Outcome),
				OutcomeAt: m.OutcomeAt,
				Title:     m.Title,
				UpdatedAt: m.UpdatedAt.UTC().Format(time.RFC3339),
			})
		}
		out = append(out, agentDeviceThreads{Device: dirID, OS: os, Threads: threads})
	}
	return out
}

// broadcastAllThreads pushes the cross-device list to every attached
// browser of every device. Lock must NOT be held.
func (s *Server) broadcastAllThreads() {
	devices := s.collectAllThreads()
	s.agentSessionsMu.Lock()
	entries := make([]*agentSessionEntry, 0, len(s.agentSessions))
	for _, entry := range s.agentSessions {
		if len(entry.subs) > 0 {
			entries = append(entries, entry)
		}
	}
	s.agentSessionsMu.Unlock()
	for _, entry := range entries {
		s.agentBroadcast(entry, agentMsg{Op: "all_threads", Devices: devices})
	}
}

// broadcastThreadList sends the thread_list snapshot to all attached
// browsers. Lock must NOT be held (broadcast takes it internally).
func (s *Server) broadcastThreadList(entry *agentSessionEntry) {
	s.agentSessionsMu.Lock()
	list := agentThreadListLocked(entry)
	s.agentSessionsMu.Unlock()
	s.agentBroadcast(entry, agentMsg{Op: "thread_list", Threads: list})
	// Any thread-list change (title, running flag, new/deleted thread) also
	// changes the cross-device sidebar, so refresh it everywhere.
	s.broadcastAllThreads()
}

// broadcastQueue sends the queue snapshot for one thread.
func (s *Server) broadcastQueue(entry *agentSessionEntry, th *agentThread) {
	s.agentSessionsMu.Lock()
	if entry.threads[th.id] != th {
		s.agentSessionsMu.Unlock()
		return
	}
	items := queueTexts(th.queue)
	ids := queueIDs(th.queue)
	modes := queueModes(th)
	s.agentSessionsMu.Unlock()
	raw, err := json.Marshal(items)
	if err != nil {
		return
	}
	s.agentBroadcast(entry, agentMsg{Op: "queue", Thread: th.id, Items: raw, QueueIDs: ids, QueueModes: modes})
}

// denyApprovalsLocked resolves every pending approval of a thread with deny
// and every pending question with a dismissal. Caller must hold
// agentSessionsMu.
func denyApprovalsLocked(th *agentThread) {
	for id, ch := range th.approvals {
		delete(th.approvals, id)
		ch <- approvalDecision{allow: false}
	}
	for id, ch := range th.questions {
		delete(th.questions, id)
		ch <- ""
	}
}

// requestQuestion implements the ask tool's question flow: broadcasts
// ask_request to the turn's viewers and waits for the browser's answer
// (dismissed on timeout, turn cancel, or turn end).
func (s *Server) requestQuestion(entry *agentSessionEntry, th *agentThread, question string, options json.RawMessage) string {
	s.agentSessionsMu.Lock()
	th.aprSeq++
	id := fmt.Sprintf("ask_%s_%d", th.id, th.aprSeq)
	ch := make(chan string, 1)
	th.questions[id] = ch
	turnCtx := th.turnCtx
	s.agentSessionsMu.Unlock()

	s.agentBroadcastTurn(entry, th, agentMsg{Op: "ask_request", ID: id, Text: question, Options: options})
	s.broadcastThreadList(entry)

	if turnCtx == nil {
		turnCtx = context.Background()
	}
	timer := time.NewTimer(agentApprovalTimeout)
	defer timer.Stop()
	resolve := func() {
		s.agentSessionsMu.Lock()
		delete(th.questions, id)
		s.agentSessionsMu.Unlock()
		s.broadcastThreadList(entry)
	}
	var answer string
	select {
	case answer = <-ch:
	case <-timer.C:
	case <-turnCtx.Done():
	}
	resolve()
	// Close the card on every attached browser, whatever the outcome.
	// Turn-scoped so mid-turn replays (auth_ok live frames) include it.
	s.agentBroadcastTurn(entry, th, agentMsg{Op: "ask_resolved", ID: id, Answer: answer})
	return answer
}

// requestApproval implements agent.Config.Approve: short-circuits on full
// permission mode / always-allow, otherwise broadcasts approval_request and
// waits for the browser's decision (deny on timeout or turn cancellation).
// A disconnected browser may reattach and answer the replayed request.
func (s *Server) requestApproval(entry *agentSessionEntry, th *agentThread, tc agent.ToolCall, args json.RawMessage, contexts ...context.Context) (bool, error) {
	s.agentSessionsMu.Lock()
	if entry.permMode == agentPermFull || th.alwaysAllow[tc.Name] {
		s.agentSessionsMu.Unlock()
		return true, nil
	}
	th.aprSeq++
	id := fmt.Sprintf("apr_%s_%d", th.id, th.aprSeq)
	ch := make(chan approvalDecision, 1)
	th.approvals[id] = ch
	turnCtx := th.turnCtx
	if len(contexts) > 0 {
		turnCtx = contexts[0]
	}
	s.agentSessionsMu.Unlock()

	s.agentBroadcastTurn(entry, th, agentMsg{
		Op:      "approval_request",
		ID:      id,
		Name:    tc.Name,
		Args:    string(args),
		Summary: tc.Summary,
	})
	s.broadcastThreadList(entry)

	if turnCtx == nil {
		turnCtx = context.Background()
	}
	timer := time.NewTimer(agentApprovalTimeout)
	defer timer.Stop()
	resolve := func() {
		s.agentSessionsMu.Lock()
		delete(th.approvals, id)
		s.agentSessionsMu.Unlock()
		s.broadcastThreadList(entry)
	}
	select {
	case d := <-ch:
		resolve()
		if d.allow && d.always {
			s.agentSessionsMu.Lock()
			th.alwaysAllow[tc.Name] = true
			s.agentSessionsMu.Unlock()
		}
		return d.allow, nil
	case <-timer.C:
		resolve()
		return false, nil
	case <-turnCtx.Done():
		resolve()
		return false, nil
	}
}

// agentThreadSnapshot is the replay state of one thread for auth_ok,
// thread_switched, thread_peeked and delete-thread remaps.
type agentThreadSnapshot struct {
	goal         *agentGoal
	loop         *agentLoop
	participants []agentParticipant

	automation *automationView
	history    []agentHistoryEntry
	todos      []todoItem
	queue      []string
	queueIDs   []string
	queueModes []string
	running    bool
	working    *agentWorking
	usage      *agentUsage
	live       []json.RawMessage
}

// threadSnapshotLocked snapshots a thread for an attacher. History and live
// are disjoint: a running turn replays its frozen pre-turn history plus live
// frames, even if context recovery replaces the session history. Idle threads
// use the complete current history. Caller must hold agentSessionsMu.
func (s *Server) threadSnapshotLocked(entry *agentSessionEntry, th *agentThread) agentThreadSnapshot {
	msgs := th.turnHistory
	if !th.running {
		msgs = th.session.History()
	}
	snap := agentThreadSnapshot{
		goal: cloneGoal(th.goal), loop: cloneLoop(th.loop),
		todos:      th.todos,
		queue:      queueTexts(th.queue),
		queueIDs:   queueIDs(th.queue),
		queueModes: queueModes(th),
		running:    th.running,
		usage:      th.lastUsage,
	}
	if th.deviceScope != nil {
		snap.participants = th.deviceScope.snapshot()
	}
	if th.automation != nil {
		th.automation.mu.Lock()
		view := th.automation.view
		snap.automation = &view
		th.automation.mu.Unlock()
	}
	if snap.todos == nil {
		snap.todos = []todoItem{}
	}
	if snap.queue == nil {
		snap.queue = []string{}
	}
	if th.running {
		snap.working = &agentWorking{Since: th.workingSince.UTC().Format(time.RFC3339Nano), Tool: th.workingTool}
		snap.live = th.live.Snapshot()
	}
	if th.autonomyPending {
		snap.live = th.live.Snapshot()
	}
	snap.history = renderAgentHistory(msgs)
	if snap.history == nil {
		snap.history = []agentHistoryEntry{}
	}
	return snap
}

// establish finishes the auth handshake: attach this socket to the
// server-owned per-device agent entry and tell the browser it can chat.
func (h *agentWSHandler) establish(ac *agentConn, client *ClientConn) {
	entry, err := h.srv.agentSessionFor(ac.deviceID, client)
	if err != nil {
		ac.sendError("agent misconfigured: " + err.Error())
		return
	}
	h.srv.agentSessionsMu.Lock()
	if entry.client != client {
		// Device reconnected since the entry was created.
		entry.client = client
	}
	// Compare against the persisted prefs (not entry.os, which was just
	// seeded from the same client) so a first-ever attach also writes.
	osLearned := ""
	if os := clientOS(client); os != "" {
		entry.os = os
		if h.srv.loadAgentPrefs(entry.deviceID).OS != os {
			osLearned = os
		}
	}
	h.srv.rebindAgentSessionLocked(entry)
	entry.subs[ac.socket] = true
	entry.lastActive = time.Now()
	// Seed this connection's current thread from the entry's remembered
	// default; afterwards the two diverge freely.
	current := entry.current
	th := entry.threads[current]
	// auth_ok reports the current thread's model (threads may differ).
	model := entry.model
	threadCwd, threadBranch := "", ""
	var snap agentThreadSnapshot
	if th != nil {
		if th.model != "" {
			model = th.model
		}
		threadCwd = agentEffectiveCwdLocked(entry, th)
		threadBranch = th.branch
		snap = h.srv.threadSnapshotLocked(entry, th)
	}
	threads := agentThreadListLocked(entry)
	permMode := entry.permMode
	reasoningLevel := entry.reasoningLevel
	h.srv.agentSessionsMu.Unlock()
	ac.entry = entry
	ac.current = current
	// Persist a freshly learned OS so the global thread list can render an
	// icon for this device even while it is offline.
	if osLearned != "" {
		h.srv.writeAgentPrefs(entry)
	}

	modelName := model
	if _, mc, _, rerr := h.srv.Agent.Resolve(model); rerr == nil {
		modelName = mc.Name
		if modelName == "" {
			modelName = mc.ID
		}
	}
	ac.writeJSON(agentMsg{
		Goal: snap.goal, Loop: snap.loop,
		Op:             "auth_ok",
		Offline:        ac.deviceID != agentServerDeviceID && client == nil,
		Device:         ac.deviceID,
		Model:          model,
		ModelName:      modelName,
		Models:         h.srv.Agent.Models(),
		History:        snap.history,
		Running:        snap.running,
		Thread:         current,
		Threads:        threads,
		Todos:          snap.todos,
		Queue:          snap.queue,
		QueueIDs:       snap.queueIDs,
		QueueModes:     snap.queueModes,
		AutoApprove:    permMode == agentPermFull, // legacy field
		PermissionMode: permMode,
		ReasoningLevel: reasoningLevel,
		Working:        snap.working,
		Cwd:            threadCwd,
		Branch:         threadBranch,
		Live:           snap.live,
		Automation:     snap.automation,
		Participants:   snap.participants,
		Usage:          snap.usage,
	})
	// The sidebar shows threads of every device (not partitioned per
	// device), so a freshly attached browser gets the global list too.
	ac.writeJSON(agentMsg{Op: "all_threads", Devices: h.srv.collectAllThreads()})
	// Probe the current thread's git branch (off-lock) and broadcast the
	// result as thread_meta.
	h.srv.scheduleThreadMetaProbe(entry, current)
}

func (h *agentWSHandler) OnClose(socket *gws.Conn, err error) {
	acRaw, _ := socket.Session().Load("agentConn")
	if acRaw == nil {
		return
	}
	ac := acRaw.(*agentConn)
	// Only detach: the turn (if any) keeps running server-side so a reopened
	// tab can reattach and receive remaining events and pending approvals.
	// Approval timeout and turn cancellation still resolve unanswered requests.
	if ac.entry != nil {
		h.srv.agentSessionsMu.Lock()
		delete(ac.entry.subs, socket)
		h.srv.agentSessionsMu.Unlock()
		h.srv.releaseQueueEdits(ac)
	}
}

func (h *agentWSHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in agent OnMessage: %v", r)
		}
	}()
	defer message.Close()

	if message.Opcode != gws.OpcodeText {
		return
	}

	acRaw, _ := socket.Session().Load("agentConn")
	if acRaw == nil {
		return
	}
	ac := acRaw.(*agentConn)

	if h.handleAgentRPCFrame(ac, message.Bytes()) {
		return
	}
	var msg agentMsg
	if err := json.Unmarshal(message.Bytes(), &msg); err != nil {
		return
	}

	// In-band device password auth (same flow as terminal.go).
	if !ac.authed {
		if msg.Op != "auth" {
			return
		}
		h.srv.mu.RLock()
		client, ok := h.srv.clients[ac.deviceID]
		h.srv.mu.RUnlock()
		if !ok {
			ac.sendError("device '" + ac.deviceID + "' disconnected")
			socket.WriteClose(1000, nil)
			return
		}
		// Brute-force throttle: reject during cooldown without comparing.
		if h.srv.authLimiter.wait(ac.deviceID) > 0 {
			ac.writeJSON(agentMsg{Op: "auth_fail", Message: "Too many attempts, retry later"})
			return
		}
		if client.Password != "" && constantTimeEqual(client.Password, msg.Password) {
			h.srv.authLimiter.success(ac.deviceID)
			ac.authed = true
			h.establish(ac, client)
		} else {
			h.srv.authLimiter.fail(ac.deviceID)
			ac.writeJSON(agentMsg{Op: "auth_fail", Message: "Wrong password"})
		}
		return
	}

	if ac.deviceID != agentServerDeviceID {
		if _, online := h.srv.GetClient(ac.deviceID); !online && !h.hasOnlineParticipant(ac, msg.Thread) && (msg.Op == "steer" || msg.Op == "promote_queue" || msg.Op == "steer_job" || msg.Op == "steer_subagent") {
			ac.sendErrorThread(msg.Thread, "Device offline: guidance and device jobs are unavailable")
			return
		}
	}
	switch msg.Op {
	case "goal", "loop":
		h.handleAutonomy(ac, msg)
	case "automation_control":
		h.handleAutomation(ac, msg)
	case "get_jobs":
		h.handleJobs(ac, msg.Thread, msg.ID, false)
	case "cancel_job":
		if msg.JobID != "" {
			h.handleCancelJob(ac, msg)
		} else {
			h.handleJobs(ac, msg.Thread, msg.ID, true)
		}
	case "cancel_subagent":
		h.handleCancelSubagent(ac, msg)
	case "get_subagents":
		h.handleGetSubagents(ac, msg)
	case "steer_job", "steer_subagent":
		h.handleJobGuidance(ac, msg)
	case "chat":
		if h.handleAutonomySlash(ac, msg.Text, msg.Thread, msg.Images) {
			return
		}
		if ac.deviceID != agentServerDeviceID {
			live, online := h.srv.GetClient(ac.deviceID)
			collaboratorsOnline := h.hasOnlineParticipant(ac, msg.Thread)
			if !online && !h.hasOnlineParticipant(ac, msg.Thread) && (!msg.OfflineConfirmed || strings.HasPrefix(strings.TrimSpace(msg.Text), "/")) {
				ac.sendErrorThread(msg.Thread, "Device offline: confirm historical-context chat first; device commands are unavailable")
				return
			}
			h.srv.agentSessionsMu.Lock()
			threadID := msg.Thread
			if threadID == "" {
				threadID = ac.current
			}
			if th := ac.entry.threads[threadID]; !online && !collaboratorsOnline && th != nil && th.running {
				h.srv.agentSessionsMu.Unlock()
				ac.sendErrorThread(threadID, "Wait for the active turn to finish before starting offline discussion")
				return
			}
			ac.entry.client = live
			h.srv.rebindAgentSessionLocked(ac.entry)
			h.srv.agentSessionsMu.Unlock()
		}
		h.handleChat(ac, msg.Text, msg.Model, msg.Thread, msg.Images)
	case "approve":
		h.handleApprove(ac, msg.ID, msg.Allow, msg.Always)
	case "ask_response":
		h.handleAskResponse(ac, msg.ID, msg.Answer)
	case "steer":
		h.handleSteer(ac, msg.Text, msg.Thread, msg.Images)
	case "hold_queue", "release_queue", "update_queue", "reorder_queue":
		h.handleQueueMutation(ac, msg)
	case "promote_queue":
		h.handlePromoteQueued(ac, msg.Thread, msg.QueueID)
	case "dequeue":
		h.handleDequeueItem(ac, msg.Thread, msg.Index, msg.QueueID, msg.ID, msg.Restore)
	case "stop":
		if ac.entry == nil {
			return
		}
		threadID := ac.current
		if msg.Thread != "" {
			threadID = msg.Thread
		}
		var cancel context.CancelFunc
		var th *agentThread
		h.srv.agentSessionsMu.Lock()
		th = ac.entry.threads[threadID]
		if th != nil {
			pauseAutonomyLocked(th)
		}
		if th != nil && th.running {
			cancel = th.cancel
			// Stop kills the pending queue too: a stopped turn must not
			// start follow-ups the user just aborted.
			th.queue = nil
			denyApprovalsLocked(th)
		}
		h.srv.agentSessionsMu.Unlock()
		if th != nil {
			h.srv.persistAutonomy(ac.entry, th)
			h.srv.publishAutonomy(ac.entry, th, "")
		}
		if cancel != nil {
			// The turn unwind emits the single stopped{canceled:true}.
			cancel()
			h.srv.broadcastQueue(ac.entry, th)
		} else {
			// Idle stop: plain ack, no transcript effect.
			ac.writeJSON(agentMsg{Op: "stopped", Thread: threadID, Canceled: false})
		}
	case "reset":
		h.handleReset(ac, msg.Thread)
	case "set_auto_approve":
		// Legacy binary toggle: true → full, false → write.
		if ac.entry == nil {
			return
		}
		mode := agentPermWrite
		if msg.Value {
			mode = agentPermFull
		}
		h.setPermissionMode(ac, mode)
	case "set_permission_mode":
		if ac.entry == nil {
			return
		}
		if !validPermMode(msg.Mode) {
			ac.sendError("unknown permission mode " + msg.Mode)
			return
		}
		h.setPermissionMode(ac, msg.Mode)
	case "set_reasoning_level":
		h.handleSetReasoningLevel(ac, msg.Level)
	case "new_thread":
		h.handleNewThread(ac)
	case "switch_thread":
		h.handleSwitchThread(ac, msg.Thread)
	case "peek_thread":
		h.handlePeekThread(ac, msg.Thread)
	case "delete_thread":
		h.handleThreadAction(ac, msg)
	case "rename_thread", "pin_thread":
		h.handleThreadAction(ac, msg)
	case "participants":
		h.handleParticipants(ac, msg)
	case "set_cwd":
		h.handleSetCwd(ac, msg.Path)
	case "get_changes":
		h.handleGetChanges(ac, msg.ID)
	case "get_diff":
		h.handleGetDiff(ac, msg.Path, msg.ID)
	case "file_read":
		h.handleFileRead(ac, msg.Path, msg.ID)
	case "dir_list":
		h.handleDirList(ac, msg.Path, msg.ID)
	case "dir_mkdir":
		h.handleDirMkdir(ac, msg.Path, msg.ID)
	case "search_files":
		h.handleSearchFiles(ac, msg.Q, msg.ID)
	case "ping":
		ac.writeJSON(agentMsg{Op: "pong"})
	}
}

// handleApprove resolves one pending approval request (by apr_id, across
// all of the device's threads).
func (h *agentWSHandler) handleApprove(ac *agentConn, id string, allow, always bool) {
	if ac.entry == nil || id == "" {
		return
	}
	h.srv.agentSessionsMu.Lock()
	for _, th := range ac.entry.threads {
		if ch, ok := th.approvals[id]; ok {
			delete(th.approvals, id)
			ch <- approvalDecision{allow: allow, always: always}
			break
		}
	}
	h.srv.agentSessionsMu.Unlock()
}

// handleAskResponse resolves one pending ask-tool question (by ask id,
// across all of the device's threads); requestQuestion broadcasts the
// ask_resolved frame once it wakes.
func (h *agentWSHandler) handleAskResponse(ac *agentConn, id, answer string) {
	if ac.entry == nil || id == "" {
		return
	}
	h.srv.agentSessionsMu.Lock()
	for _, th := range ac.entry.threads {
		if ch, ok := th.questions[id]; ok {
			delete(th.questions, id)
			ch <- answer
			break
		}
	}
	h.srv.agentSessionsMu.Unlock()
}

// setPermissionMode switches the entry's approval mode and broadcasts the
// change to all attached browsers.
func (h *agentWSHandler) setPermissionMode(ac *agentConn, mode string) {
	h.srv.agentSessionsMu.Lock()
	ac.entry.permMode = mode
	h.srv.agentSessionsMu.Unlock()
	h.srv.writeAgentPrefs(ac.entry)
	h.srv.agentBroadcast(ac.entry, agentMsg{Op: "permission_mode", Mode: mode})
}

// handleSetReasoningLevel switches the entry's reasoning effort and rebuilds
// idle threads' sessions (history preserved) so the next turn picks it up;
// running threads rebuild when their next turn starts.
func (h *agentWSHandler) handleSetReasoningLevel(ac *agentConn, level string) {
	entry := ac.entry
	if entry == nil {
		return
	}
	if !validReasoningLevel(level) {
		ac.sendError("unknown reasoning level " + level)
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	srv.setReasoningLevelLocked(entry, level)
	srv.agentSessionsMu.Unlock()
	srv.writeAgentPrefs(entry)
	srv.agentBroadcast(entry, agentMsg{Op: "reasoning_level", Level: level})
}

// Caller holds agentSessionsMu so read-modify-write controls stay atomic.
func (srv *Server) setReasoningLevelLocked(entry *agentSessionEntry, level string) {
	entry.reasoningLevel = level
	for _, th := range entry.threads {
		if th.running {
			th.pendingReasoning = true
			continue
		}
		// Each thread keeps its own model across the rebuild.
		provider, model, err := srv.resolveAgentModel(th.model)
		if err != nil {
			continue
		}
		newSession := srv.buildAgentSession(entry, th, entry.client, provider, model)
		newSession.SetHistory(th.session.History())
		th.session = newSession
		th.sessionClient = entry.client
	}
}

func (h *agentWSHandler) handleChat(ac *agentConn, text, modelRef, threadRef string, imagesRaw json.RawMessage) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	images, err := parseAgentImages(imagesRaw)
	if err != nil {
		ac.sendError(err.Error())
		return
	}
	h.handleChatWithImages(ac, entry, text, modelRef, threadRef, images)
}

// Chat and idle steering share admission after decoding attachments once.
func (h *agentWSHandler) handleChatWithImages(ac *agentConn, entry *agentSessionEntry, text, modelRef, threadRef string, images []agent.MessageImage) {
	if request, ok := autonomySlash(text); ok {
		request.Thread = threadRef
		if len(images) > 0 {
			ac.writeJSON(agentMsg{Op: "autonomy", Thread: threadRef, Message: "autonomy commands do not accept images"})
			return
		}
		h.handleAutonomy(ac, request)
		return
	}
	srv := h.srv
	// One lock hold covers the queue check and the turn start: two chats
	// arriving together can never both start a turn (one queues).
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	threadID := ac.current
	if threadRef != "" && threadRef != ac.current {
		// Split-screen panes chat into threads they do not drive; the turn
		// frames are tagged with the thread id, so every pane stays in sync.
		th = entry.threads[threadRef]
		threadID = threadRef
	}
	if th == nil {
		srv.agentSessionsMu.Unlock()
		if threadRef != "" && threadRef != ac.current {
			ac.sendErrorThread(threadRef, "unknown thread "+threadRef)
		} else {
			ac.sendError("agent session not ready")
		}
		return
	}
	cancelAutonomyPendingLocked(th)
	th.goalActivity = ""
	if th.loop != nil && th.loop.Status == "active" && th.loop.Prompt == "" {
		th.loop.Prompt = text
		th.loop.Iteration++
	}
	if th.running || (len(th.queue) > 0 && (entry.deviceID == agentServerDeviceID || entry.client != nil)) {
		// Queue the message; it becomes a follow-up turn when the current
		// one finishes. Queued chats remember their requested model.
		th.queue = append(th.queue, agentQueuedChat{text: text, images: images, model: modelRef})
		entry.lastActive = time.Now()
		srv.agentSessionsMu.Unlock()
		srv.broadcastQueue(entry, th)
		srv.resumeAgentQueue(entry, th)
		return
	}
	// Pick up a device reconnect that happened while a turn was running.
	srv.rebindAgentSessionLocked(entry)
	// Optional model switch: resolve before touching state so an unknown
	// model aborts without running.
	modelChanged := false
	if modelRef != "" && modelRef != th.model {
		provider, model, canonical, err := srv.Agent.Resolve(modelRef)
		if err != nil {
			srv.agentSessionsMu.Unlock()
			ac.sendErrorThread(threadID, "unknown model "+modelRef)
			return
		}
		// Rebuild the session for the new provider/model, preserving history.
		newSession := srv.buildAgentSession(entry, th, entry.client, provider, model)
		newSession.SetHistory(th.session.History())
		th.session = newSession
		th.sessionClient = entry.client
		th.model = canonical
		entry.model = canonical // last-used default for new threads
		modelChanged = true
	}
	th.updatedAt = time.Now()
	startFrame := srv.startAgentTurnLocked(entry, th, text, images)
	turn := snapshotAgentTurnLocked(th)
	srv.agentSessionsMu.Unlock()

	if modelChanged {
		srv.writeAgentPrefs(entry)
	}
	srv.agentBroadcastTurn(entry, th, startFrame)
	srv.goAgent(func() { srv.runAgentTurn(entry, th, turn, text, images) })
}

// handleSteer prioritizes input at the next settled model/tool boundary.
// When idle it behaves like a normal chat; commands stay in the turn queue.
func (h *agentWSHandler) handleSteer(ac *agentConn, text, threadRef string, imagesRaw json.RawMessage) {
	if h.handleAutonomySlash(ac, text, threadRef, imagesRaw) {
		return
	}
	text = strings.TrimSpace(text)
	images, err := parseAgentImages(imagesRaw)
	if err != nil {
		ac.sendError(err.Error())
		return
	}
	if text == "" && len(images) == 0 {
		return
	}
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	threadID := ac.current
	if threadRef != "" {
		threadID = threadRef
	}
	th := entry.threads[threadID]
	if th == nil || !th.running {
		srv.agentSessionsMu.Unlock()
		h.handleChatWithImages(ac, entry, text, "", threadRef, images)
		return
	}
	queueAgentSteeringLocked(th, agentQueuedChat{text: text, images: images})
	srv.agentSessionsMu.Unlock()
	srv.broadcastQueue(entry, th)
}

// handleDequeue retains the legacy index operation for older callers.
// Current browsers use queue identity plus a correlated acknowledgement.
func (h *agentWSHandler) handleDequeue(ac *agentConn, threadRef string, index int) {
	h.handleDequeueItem(ac, threadRef, index, "", "", false)
}

func (h *agentWSHandler) handleDequeueItem(ac *agentConn, threadRef string, index int, queueID, requestID string, restore bool) {
	entry := ac.entry
	if entry == nil {
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	threadID := ac.current
	if threadRef != "" {
		threadID = threadRef
	}
	th := entry.threads[threadID]
	var removed *agentQueuedChat
	if th != nil {
		if queueID != "" {
			index = -1
			for i := range th.queue {
				if th.queue[i].id == queueID {
					index = i
					break
				}
			}
		}
		if index >= 0 && index < len(th.queue) {
			item := takeQueuedChat(&th.queue, index)
			removed = &item
			entry.lastActive = time.Now()
		}
	}
	srv.agentSessionsMu.Unlock()
	if requestID != "" {
		reply := agentMsg{Op: "dequeued", Thread: threadID, ID: requestID, QueueID: queueID, Removed: removed != nil}
		if removed != nil {
			reply.Text = removed.text
			if restore && len(removed.images) > 0 {
				reply.Images, _ = json.Marshal(removed.images)
			}
		}
		ac.writeJSON(reply)
	}
	if th != nil {
		srv.broadcastQueue(entry, th)
		if removed != nil && removed.editToken != "" {
			srv.resumeAgentQueue(entry, th)
		}
	}
}

// handleReset clears a thread's history, todos and stored file. A running
// turn is canceled via the epoch mechanism: its unwind skips the save and
// the terminal broadcast, so the cleared state is never resurrected.
func (h *agentWSHandler) handleReset(ac *agentConn, threadRef string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	threadID := ac.current
	if threadRef != "" {
		threadID = threadRef
	}
	srv := h.srv
	th := srv.lockAgentThreadPersistence(entry, threadID)
	if th == nil {
		ac.sendErrorThread(threadID, "unknown thread "+threadID)
		return
	}
	defer th.persistMu.Unlock()
	cancel := th.cancel
	oldAutomation := th.automation
	th.automation = nil
	cancelAutonomyPendingLocked(th)
	th.goal, th.loop = nil, nil
	th.goalActivity = ""
	th.epoch++
	th.scheduled = nil
	th.running = false
	th.turnHistory = nil
	th.cancel = nil
	th.turnCtx = nil
	th.workingTool = ""
	if th.jobs != nil {
		th.jobs.Close()
		th.jobs = nil
	}
	// Fresh session with the thread's own model and an empty history.
	provider, model, _, err := srv.Agent.Resolve(th.model)
	if err != nil {
		provider, model, _, _ = srv.Agent.Resolve("")
	}
	th.session = srv.buildAgentSession(entry, th, entry.client, provider, model)
	th.sessionClient = entry.client
	th.todos = nil
	th.todoPhases = nil
	th.turnFiles = nil
	th.queue = nil
	th.lastUsage = nil
	th.lastOutcome = ""
	th.outcomeAt = ""
	th.alwaysAllow = make(map[string]bool)
	th.branch = ""
	th.live.Reset()
	th.updatedAt = time.Now()
	denyApprovalsLocked(th)
	srv.agentSessionsMu.Unlock()
	srv.invalidateScheduledThread(entry.deviceID, threadID, "target thread reset")
	srv.retireAutomation(oldAutomation)
	if cancel != nil {
		cancel()
	}
	if store := srv.agentStore(); store != nil {
		store.clear(ac.deviceID, threadID)
	}
	srv.agentBroadcast(entry, agentMsg{Op: "reset_ok", Thread: threadID})
	srv.publishAutonomy(entry, th, "")
	srv.broadcastThreadList(entry)
}

// handleNewThread creates an empty thread and switches to it.
func (h *agentWSHandler) handleNewThread(ac *agentConn) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := srv.newAgentThreadLocked(entry, newAgentThreadID(), nil, "", "")
	th.persistMu.Lock() // new thread is not published yet; no competing owner
	entry.threads[th.id] = th
	entry.current = th.id // resume default for future connections
	ac.current = th.id
	srv.agentSessionsMu.Unlock()

	// Persist explicit empty conversations too, so restoring a split after a
	// server restart does not silently replace its newly-created thread.
	srv.agentSessionsMu.Lock()
	current := entry.threads[th.id] == th
	model := th.model
	srv.agentSessionsMu.Unlock()
	if current {
		if store := srv.agentStore(); store != nil {
			store.replace(entry.deviceID, th.id, model, nil)
		}
	}
	th.persistMu.Unlock()

	srv.writeAgentPrefs(entry)
	srv.broadcastThreadList(entry)
	// Only the requesting connection switches; other browsers keep their
	// current thread.
	ac.writeJSON(agentMsg{
		Op:      "thread_switched",
		Thread:  th.id,
		History: []agentHistoryEntry{},
		Todos:   []todoItem{},
		Queue:   []string{},
	})
	srv.scheduleThreadMetaProbe(entry, th.id)
}

// handleSwitchThread makes another thread current and replays its state.
func (h *agentWSHandler) handleSwitchThread(ac *agentConn, threadID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th, ok := entry.threads[threadID]
	if !ok {
		srv.agentSessionsMu.Unlock()
		ac.sendErrorThread(threadID, "unknown thread "+threadID)
		return
	}
	if threadID == ac.current {
		// Already viewing this thread (double-click, retry, or the auth
		// URL adoption where auth_ok already delivered the snapshot):
		// replaying history again would flash the transcript.
		srv.agentSessionsMu.Unlock()
		return
	}
	entry.current = threadID // resume default
	ac.current = threadID
	snap := srv.threadSnapshotLocked(entry, th)
	model := th.model
	srv.agentSessionsMu.Unlock()

	// thread_switched is per-connection: only the requester's view changes.
	ac.writeJSON(agentMsg{
		Goal: snap.goal, Loop: snap.loop,
		Op:           "thread_switched",
		Thread:       threadID,
		Model:        model,
		History:      snap.history,
		Todos:        snap.todos,
		Queue:        snap.queue,
		QueueIDs:     snap.queueIDs,
		QueueModes:   snap.queueModes,
		Running:      snap.running,
		Working:      snap.working,
		Live:         snap.live,
		Automation:   snap.automation,
		Participants: snap.participants,
		Usage:        snap.usage,
	})
	srv.writeAgentPrefs(entry)
	srv.broadcastThreadList(entry)
	srv.scheduleThreadMetaProbe(entry, threadID)
}

// handlePeekThread replays a thread's state (history + live frames) to the
// requester WITHOUT making it current: split-screen panes subscribe to
// threads they do not drive. Turn frames are already broadcast to all conns
// tagged with their thread id, so after this seed the browser routes them by
// thread id itself.
func (h *agentWSHandler) handlePeekThread(ac *agentConn, threadID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th, ok := entry.threads[threadID]
	if !ok {
		srv.agentSessionsMu.Unlock()
		ac.sendErrorThread(threadID, "unknown thread "+threadID)
		return
	}
	snap := srv.threadSnapshotLocked(entry, th)
	srv.agentSessionsMu.Unlock()

	ac.writeJSON(agentMsg{
		Goal: snap.goal, Loop: snap.loop,
		Op:           "thread_peeked",
		Thread:       threadID,
		History:      snap.history,
		Todos:        snap.todos,
		Queue:        snap.queue,
		QueueIDs:     snap.queueIDs,
		QueueModes:   snap.queueModes,
		Running:      snap.running,
		Working:      snap.working,
		Live:         snap.live,
		Automation:   snap.automation,
		Participants: snap.participants,
		Usage:        snap.usage,
	})
}

// connsViewThreadLocked reports whether any subscribed connection has the
// given thread as its current. Caller holds agentSessionsMu.
func connsViewThreadLocked(entry *agentSessionEntry, threadID string) bool {
	for sock := range entry.subs {
		sub, _ := sock.Session().Load("agentConn")
		if conn, _ := sub.(*agentConn); conn != nil && conn.current == threadID {
			return true
		}
	}
	return false
}

// handleDeleteThread cancels active work and removes a thread. Every
// connection viewing the deleted thread is switched to the most recently
// updated remaining one, or a fresh thread when none is left.
func (h *agentWSHandler) handleDeleteThread(ac *agentConn, threadID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	th := srv.lockAgentThreadPersistence(entry, threadID)
	if th == nil {
		ac.sendError("unknown thread " + threadID)
		return
	}
	defer th.persistMu.Unlock()
	// Invalidate the old generation before canceling: its late events and
	// final save cannot recreate the deleted conversation.
	th.epoch++
	cancelAutonomyPendingLocked(th)
	cancel := th.cancel
	oldAutomation := th.automation
	th.automation = nil
	th.queue = nil
	th.turnFiles = nil
	denyApprovalsLocked(th)
	if th.jobs != nil {
		th.jobs.Close()
	}
	delete(entry.threads, threadID)
	// Remap every connection viewing the deleted thread (plus the resume
	// default) to the most recently updated remaining one, or a fresh
	// thread when none is left.
	var affected []*agentConn
	switchedTo := ""
	if entry.current == threadID || connsViewThreadLocked(entry, threadID) {
		var best *agentThread
		for _, other := range entry.threads {
			if best == nil || other.updatedAt.After(best.updatedAt) {
				best = other
			}
		}
		if best == nil {
			best = srv.newAgentThreadLocked(entry, newAgentThreadID(), nil, "", "")
			entry.threads[best.id] = best
		}
		switchedTo = best.id
		if entry.current == threadID {
			entry.current = best.id
		}
		for sock := range entry.subs {
			sub, _ := sock.Session().Load("agentConn")
			conn, _ := sub.(*agentConn)
			if conn == nil || conn.current != threadID {
				continue
			}
			conn.current = best.id
			affected = append(affected, conn)
		}
	}
	var snap agentThreadSnapshot
	var switchModel string
	if switchedTo != "" {
		cur := entry.threads[switchedTo]
		snap = srv.threadSnapshotLocked(entry, cur)
		switchModel = cur.model
	}
	srv.agentSessionsMu.Unlock()
	srv.invalidateScheduledThread(entry.deviceID, threadID, "target thread deleted")
	srv.retireAutomation(oldAutomation)
	if cancel != nil {
		cancel()
	}

	if store := srv.agentStore(); store != nil {
		store.clear(ac.deviceID, threadID)
		store.dropExtras(ac.deviceID, threadID)
	}
	ac.writeJSON(agentMsg{Op: "thread_deleted", Thread: threadID, Device: ac.deviceID})
	// The request acknowledgment carries its correlation ID. The separate
	// notification also lets other attached browsers discard local drafts.
	srv.agentBroadcast(entry, agentMsg{Op: "thread_deleted", Thread: threadID, Device: ac.deviceID})
	srv.broadcastThreadList(entry)
	if switchedTo != "" {
		srv.writeAgentPrefs(entry)
		// Each affected connection is switched individually; unaffected
		// browsers stay on their thread.
		for _, conn := range affected {
			conn.writeJSON(agentMsg{
				Goal: snap.goal, Loop: snap.loop,
				Op:           "thread_switched",
				Thread:       switchedTo,
				Model:        switchModel,
				History:      snap.history,
				Todos:        snap.todos,
				Queue:        snap.queue,
				QueueIDs:     snap.queueIDs,
				QueueModes:   snap.queueModes,
				Running:      snap.running,
				Working:      snap.working,
				Live:         snap.live,
				Automation:   snap.automation,
				Participants: snap.participants,
				Usage:        snap.usage,
			})
		}
		srv.scheduleThreadMetaProbe(entry, switchedTo)
	}
}

// handleRenameThread sets a thread's explicit title (persisted in the meta
// line) and rebroadcasts the thread list.
func (h *agentWSHandler) handleRenameThread(ac *agentConn, threadID, title string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		ac.sendError("title must not be empty")
		return
	}
	srv := h.srv
	th := srv.lockAgentThreadPersistence(entry, threadID)
	if th == nil {
		ac.sendError("unknown thread " + threadID)
		return
	}
	defer th.persistMu.Unlock()
	th.title = title
	history := th.session.History()
	srv.agentSessionsMu.Unlock()

	if store := srv.agentStore(); store != nil {
		store.setTitle(ac.deviceID, threadID, title, history)
	}
	ac.writeJSON(agentMsg{Op: "thread_renamed", Thread: threadID, Title: title, Device: ac.deviceID})
	srv.broadcastThreadList(entry)
}

// handleSetCwd sets the current thread's working directory ("" restores the
// device default): the session is rebuilt so tools and the system prompt
// pick it up, the meta line is rewritten, and a branch probe is scheduled.
func (h *agentWSHandler) handleSetCwd(ac *agentConn, path string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	srv := h.srv
	th := srv.lockAgentThreadPersistence(entry, ac.current)
	if th == nil {
		ac.sendError("agent session not ready")
		return
	}
	defer th.persistMu.Unlock()
	if th.running {
		srv.agentSessionsMu.Unlock()
		ac.sendError("agent is busy; stop first")
		return
	}
	th.cwd = path
	if th.deviceScope != nil {
		th.deviceScope.mu.Lock()
		th.deviceScope.members[entry.deviceID] = path
		th.deviceScope.mu.Unlock()
	}
	th.branch = ""
	// Invalidate an already-running probe immediately. This matters when a
	// probe for the same path races with a rapid cwd reset before the next
	// probe has been scheduled.
	th.metaProbeSeq++
	// Rebuild the session so tools and the system prompt use the new cwd;
	// the thread keeps its own model.
	if provider, model, err := srv.resolveAgentModel(th.model); err == nil {
		newSession := srv.buildAgentSession(entry, th, entry.client, provider, model)
		newSession.SetHistory(th.session.History())
		th.session = newSession
		th.sessionClient = entry.client
	}
	history := th.session.History()
	threadID := th.id
	cwd := agentEffectiveCwdLocked(entry, th)
	srv.agentSessionsMu.Unlock()

	if store := srv.agentStore(); store != nil {
		store.setCwd(ac.deviceID, threadID, path, history)
	}
	srv.agentBroadcast(entry, agentMsg{Op: "thread_meta", Thread: threadID, Cwd: cwd})
	srv.scheduleThreadMetaProbe(entry, threadID)
}

// handleGetChanges replies with the current thread's git working-tree
// status. The probe round-trips the device, so it runs off the socket
// goroutine; failures come back in the reply's error field.
func (h *agentWSHandler) handleGetChanges(ac *agentConn, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	if th == nil {
		srv.agentSessionsMu.Unlock()
		ac.sendError("agent session not ready")
		return
	}
	cwd := agentEffectiveCwdLocked(entry, th)
	branch := th.branch
	threadID := th.id
	srv.agentSessionsMu.Unlock()

	srv.goAgent(func() {
		if cwd == "" && th != nil {
			cwd = srv.awaitAgentDefaultCwd(entry)
		}
		reply := agentChangesMsg{Op: "changes", ID: requestID, Thread: threadID, Cwd: cwd, Branch: branch, Files: []agentChangeFile{}}
		if cwd == "" {
			reply.Error = "no working directory"
		} else if files, err := srv.probeGitChanges(entry.deviceID, cwd); err != nil {
			reply.Error = err.Error()
		} else {
			reply.Files = files
		}
		ac.writeChanges(reply)
	})
}

// handleGetDiff replies with the unified diff of one path in the current
// thread's working tree; untracked files come back with error "untracked".
func (h *agentWSHandler) handleGetDiff(ac *agentConn, path, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		ac.writeDiff(agentDiffMsg{Op: "diff", ID: requestID, Error: "missing path"})
		return
	}
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	cwd, threadID := "", ""
	if th != nil {
		cwd = agentEffectiveCwdLocked(entry, th)
		threadID = th.id
	}
	srv.agentSessionsMu.Unlock()

	srv.goAgent(func() {
		if cwd == "" && th != nil {
			cwd = srv.awaitAgentDefaultCwd(entry)
		}
		reply := agentDiffMsg{Op: "diff", ID: requestID, Thread: threadID, Path: path}
		if cwd == "" {
			reply.Error = "no working directory"
		} else if diff, err := srv.probeGitDiff(entry.deviceID, cwd, path); err != nil {
			reply.Error = err.Error()
		} else {
			reply.Diff = diff
		}
		ac.writeDiff(reply)
	})
}

// handleFileRead answers one browser's file_read op with the file's content
// from the current thread's working directory (op "file_content", reply to
// the requesting socket only). Powers the right-panel file preview tabs.
func (h *agentWSHandler) handleFileRead(ac *agentConn, path, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	cwd, threadID := "", ""
	if th != nil {
		cwd = agentEffectiveCwdLocked(entry, th)
		threadID = th.id
	}
	srv.agentSessionsMu.Unlock()

	reply := agentMsg{Op: "file_content", ID: requestID, Thread: threadID, Path: path}
	if path == "" {
		reply.Message = "missing path"
		ac.writeJSON(reply)
		return
	}
	srv.goAgent(func() {
		if cwd == "" && th != nil {
			cwd = srv.awaitAgentDefaultCwd(entry)
		}
		switch {
		case cwd == "":
			reply.Message = "no working directory"
		case streamPreviewMIME(path) != "":
			client, ok := srv.GetClient(entry.deviceID)
			if !ok {
				reply.Message = "device offline"
				break
			}
			url, err := srv.grantFilePreview(client, resolveDevicePath(clientOS(client), cwd, path))
			if err != nil {
				reply.Message = err.Error()
			} else {
				reply.Content, reply.Encoding, reply.Mime = url, "url", streamPreviewMIME(path)
			}
		case imageMIME(path) != "":
			/* Image preview: base64-encoded body + mime; the browser renders
			 * an <img> instead of the text view. */
			b64, err := srv.probeReadImage(entry.deviceID, cwd, path)
			if err != nil {
				reply.Message = err.Error()
			} else {
				reply.Content = b64
				reply.Encoding = "base64"
				reply.Mime = imageMIME(path)
			}
		default:
			content, truncated, err := srv.probeReadFile(entry.deviceID, cwd, path)
			switch {
			case err != nil:
				reply.Message = err.Error()
			default:
				reply.Content = content
				reply.Truncated = truncated
				if isHTMLPreviewPath(path) && !truncated {
					if client, ok := srv.GetClient(entry.deviceID); ok {
						reply.PreviewURL, _ = srv.grantFilePreview(client, resolveDevicePath(clientOS(client), cwd, path))
					}
				}
			}
		}
		ac.writeJSON(reply)
	})
}

// imageMIME maps a path's extension to a browser-renderable image mime, or
// "" for non-images (kept conservative: only formats <img> can decode).
func imageMIME(path string) string {
	ext := ""
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		ext = strings.ToLower(path[i+1:])
	}
	switch ext {
	case "png", "apng":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	case "bmp":
		return "image/bmp"
	case "ico":
		return "image/x-icon"
	case "avif":
		return "image/avif"
	}
	return ""
}

// Image preview caps: 4 MiB of raw bytes ≈ 5.6 MB of base64.
const (
	agentImageReadCap = 4 << 20
	agentImageB64Cap  = (agentImageReadCap+2)/3*4 + 8
)

// probeReadImage base64-encodes an image file on the device (encode happens
// device-side so the wire frame stays printable). Runs off-lock.
func (s *Server) probeReadImage(deviceID, cwd, path string) (string, error) {
	client, ok := s.GetClient(deviceID)
	if !ok {
		return "", fmt.Errorf("device offline")
	}
	out, code, err := s.runDeviceProbe(client, imageReadProbeCommand(clientOS(client), cwd, path), agentFileReadTimeout, agentImageB64Cap+1024)
	if err != nil {
		return "", err
	}
	if code > 0 {
		return "", fmt.Errorf("cannot read %s", path)
	}
	/* GNU base64 wraps at 76 columns; strip the wraps server-side (a tr pipe
	 * device-side would mask base64's exit code behind tr's). */
	out = strings.ReplaceAll(out, "\n", "")
	if len(out) > agentImageB64Cap {
		return "", fmt.Errorf("image too large (over %d MiB)", agentImageReadCap>>20)
	}
	return out, nil
}

// imageReadProbeCommand builds the base64 read probe for the device OS;
// Windows uses .NET's Convert. Line wraps are stripped by the caller.
func imageReadProbeCommand(goos, cwd, path string) string {
	if goos == "windows" {
		q := func(s string) string { return strings.ReplaceAll(s, `'`, `''`) }
		return fmt.Sprintf(`powershell -NoProfile -Command "Set-Location -LiteralPath '%s'; [Console]::Write([Convert]::ToBase64String([IO.File]::ReadAllBytes('%s')))"`, q(cwd), q(path))
	}
	return "cd " + shellQuote(cwd) + " && base64 < " + shellQuote(path)
}

// agentFileSearchCap caps the raw `git ls-files` probe output (~17k paths);
// agentFileSearchLimit caps the filtered reply list.
const (
	agentFileSearchCap   = 512 * 1024
	agentFileSearchLimit = 50
)

// handleSearchFiles answers one browser's search_files op with the repo
// paths matching the query (op "file_search", reply to the requesting
// socket only). Powers the right-panel new-tab file search (bb's
// NewTabFileSearch). Git repos only; every query word must appear in the
// path (case-insensitive), basename hits rank first.
func (h *agentWSHandler) handleSearchFiles(ac *agentConn, q, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	q = strings.TrimSpace(q)
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	cwd, threadID := "", ""
	if th != nil {
		cwd = agentEffectiveCwdLocked(entry, th)
		threadID = th.id
	}
	srv.agentSessionsMu.Unlock()

	reply := agentMsg{Op: "file_search", ID: requestID, Thread: threadID, Q: q, Files: []string{}}
	if q == "" {
		ac.writeJSON(reply)
		return
	}
	h.srv.goAgent(func() {
		if cwd == "" && th != nil {
			cwd = srv.awaitAgentDefaultCwd(entry)
		}
		if cwd == "" {
			reply.Message = "no working directory"
			ac.writeJSON(reply)
			return
		}
		client, ok := srv.GetClient(entry.deviceID)
		if !ok {
			reply.Message = "device offline"
			ac.writeJSON(reply)
			return
		}
		out, code, err := srv.runDeviceProbe(client, gitLsFilesAllProbeCommand(clientOS(client), cwd), agentGitProbeTimeout, agentFileSearchCap)
		switch {
		case err != nil:
			reply.Message = err.Error()
		case code > 0:
			reply.Message = "not a git repository"
		default:
			paths := strings.FieldsFunc(out, func(r rune) bool { return r == '\n' })
			files, dropped := filterFileSearch(paths, q, agentFileSearchLimit)
			reply.Files = files
			reply.Truncated = dropped
		}
		ac.writeJSON(reply)
	})
}

// filterFileSearch AND-matches the lowercase query words against each path
// (case-insensitive). Results sort basename-word hits first, then shorter
// paths. The bool reports whether the limit dropped matches.
func filterFileSearch(paths []string, q string, limit int) ([]string, bool) {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return []string{}, false
	}
	type hit struct {
		path      string
		baseMatch bool
	}
	hits := []hit{}
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if _, exists := seen[p]; exists {
			continue
		}
		seen[p] = struct{}{}
		lower := strings.ToLower(p)
		ok := true
		for _, w := range words {
			if !strings.Contains(lower, w) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		base := lower
		if i := strings.LastIndex(lower, "/"); i >= 0 {
			base = lower[i+1:]
		}
		baseMatch := false
		for _, w := range words {
			if strings.Contains(base, w) {
				baseMatch = true
				break
			}
		}
		hits = append(hits, hit{path: p, baseMatch: baseMatch})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].baseMatch != hits[j].baseMatch {
			return hits[i].baseMatch
		}
		if len(hits[i].path) != len(hits[j].path) {
			return len(hits[i].path) < len(hits[j].path)
		}
		li, lj := strings.ToLower(hits[i].path), strings.ToLower(hits[j].path)
		if li != lj {
			return li < lj
		}
		return hits[i].path < hits[j].path
	})
	dropped := false
	if len(hits) > limit {
		hits = hits[:limit]
		dropped = true
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.path)
	}
	return out, dropped
}

// handleDirList answers one browser's dir_list op with the child entries of
// a directory on the device (op "dir_entries", reply to the requesting
// socket only). An empty path resolves to the current thread's effective
// working directory. Powers the create-project path browser (bb's
// RemotePathBrowser).
func (h *agentWSHandler) handleDirList(ac *agentConn, path, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	srv := h.srv
	srv.agentSessionsMu.Lock()
	th := entry.threads[ac.current]
	cwd, threadID := "", ""
	if th != nil {
		cwd = agentEffectiveCwdLocked(entry, th)
		threadID = th.id
	}
	srv.agentSessionsMu.Unlock()

	reply := agentMsg{Op: "dir_entries", ID: requestID, Thread: threadID, Path: path}
	h.srv.goAgent(func() {
		if path == "" {
			if cwd == "" && th != nil {
				cwd = srv.awaitAgentDefaultCwd(entry)
			}
			path = cwd
			reply.Path = path
		}
		if path == "" {
			reply.Message = "no working directory"
			ac.writeJSON(reply)
			return
		}
		parent, entries, roots, err := srv.probeListDir(entry.deviceID, path)
		if err != nil {
			reply.Message = err.Error()
		} else {
			reply.Parent = parent
			reply.Entries = entries
			reply.Roots = roots
		}
		ac.writeJSON(reply)
	})
}

// handleDirMkdir answers one browser's dir_mkdir op, creating the directory
// (and parents) on the device (op "dir_mkdir", reply to the requesting
// socket only). Powers the path browser's "New folder" action.
func (h *agentWSHandler) handleDirMkdir(ac *agentConn, path, requestID string) {
	entry := ac.entry
	if entry == nil {
		ac.sendError("agent session not ready")
		return
	}
	path = strings.TrimSpace(path)
	threadID := ""
	h.srv.agentSessionsMu.Lock()
	if th := entry.threads[ac.current]; th != nil {
		threadID = th.id
	}
	h.srv.agentSessionsMu.Unlock()

	reply := agentMsg{Op: "dir_mkdir", ID: requestID, Thread: threadID, Path: path}
	if path == "" {
		reply.Message = "missing path"
		ac.writeJSON(reply)
		return
	}
	h.srv.goAgent(func() {
		if err := h.srv.probeMkdir(entry.deviceID, path); err != nil {
			reply.Message = err.Error()
		}
		ac.writeJSON(reply)
	})
}

// startAgentTurnLocked performs the running-state transition of a new turn
// and returns the turn_start frame to broadcast after unlocking. Caller
// must hold agentSessionsMu.
func (s *Server) startAgentTurnLocked(entry *agentSessionEntry, th *agentThread, text string, images []agent.MessageImage, display ...json.RawMessage) agentMsg {
	cancelAutonomyPendingLocked(th)
	th.turnAutonomy = ""
	if th.pendingReasoning {
		if provider, model, err := s.resolveAgentModel(th.model); err == nil {
			session := s.buildAgentSession(entry, th, entry.client, provider, model)
			session.SetHistory(th.session.History())
			th.session = session
			th.sessionClient = entry.client
		}
	}
	th.running = true
	command, _ := splitAgentCommand(text)
	th.turnCommand = isAgentSessionCommand(command)
	th.lastOutcome = ""
	th.turnFiles = nil
	th.toolPaths = make(map[string]string)
	th.workingSince = time.Now()
	th.workingTool = ""
	if command, _ := splitAgentCommand(text); command == "/compact" {
		th.workingTool = "/compact"
	}
	th.updatedAt = th.workingSince
	th.live.Reset()
	th.turnHistory = th.session.History()
	th.turnHistIdx = len(th.turnHistory)
	entry.lastActive = th.workingSince
	ctx, cancel := context.WithCancel(context.Background())
	th.turnCtx = ctx
	th.cancel = cancel

	metadata := map[string]any{}
	if len(display) > 0 && len(display[0]) > 0 {
		_ = json.Unmarshal(display[0], &metadata)
	}
	metadata["time"] = th.workingSince.UnixMilli()
	th.turnDisplay, _ = json.Marshal(metadata)
	start := agentMsg{Display: th.turnDisplay, Op: "turn_start", Text: text, ImagePreviews: agentImagePreviews(images)}
	if names := agentImageNames(images); len(names) > 0 {
		if raw, err := json.Marshal(names); err == nil {
			start.Images = raw
		}
	}
	return start
}

// startAgentTurn marks the thread running and spawns the turn goroutine.
func (s *Server) startAgentTurn(entry *agentSessionEntry, th *agentThread, text string, images []agent.MessageImage) {
	s.agentSessionsMu.Lock()
	startFrame := s.startAgentTurnLocked(entry, th, text, images)
	turn := snapshotAgentTurnLocked(th)
	s.agentSessionsMu.Unlock()

	s.agentBroadcastTurn(entry, th, startFrame)
	s.goAgent(func() { s.runAgentTurn(entry, th, turn, text, images) })
}

// runAgentTurn executes one turn to completion, broadcasts its lifecycle
// events, persists the history, then dequeues the next queued turn.
//
// The goroutine only streams frames and computes; every turn-state
// transition happens under agentSessionsMu: the terminal frame is appended
// to th.live and running flips in one lock hold, so no attach/snapshot can
// miss it (broadcast after unlock).
func (s *Server) runAgentTurn(entry *agentSessionEntry, th *agentThread, turn agentTurnSnapshot, text string, images []agent.MessageImage) {
	ctx, sess, cancel := turn.ctx, turn.session, turn.cancel
	startEpoch, turnModel := turn.epoch, turn.model
	cleanedAutomation := false
	cleanupAutomation := func() {
		if !cleanedAutomation {
			cleanedAutomation = true
			s.finishAutomationTurn(turn.automation)
		}
	}
	defer cleanupAutomation()
	// A reset/delete or panic must also settle the originating RPC exactly once.
	var rpcData any
	rpcErr := fmt.Errorf("conversation was canceled or replaced")
	defer func() {
		if turn.scheduled != nil && turn.scheduled.result.State == "" {
			turn.scheduled.finish(ctx, rpcErr)
		}
	}()
	defer func() {
		if turn.rpc != nil {
			if rpcErr != nil {
				turn.rpc.write(false, nil, "execution_error", rpcErr.Error())
			} else {
				turn.rpc.write(true, rpcData, "", "")
			}
		}
	}()
	// A panic anywhere in the turn must not wedge the thread: without this,
	// th.running would stay true forever, rejecting every future chat.
	defer func() {
		if r := recover(); r != nil {
			cleanupAutomation()
			rpcErr = fmt.Errorf("internal error: %v", r)
			log.Printf("PANIC in agent turn for %s/%s: %v", entry.deviceID, th.id, r)
			s.agentSessionsMu.Lock()
			current := th.epoch == startEpoch && entry.threads[th.id] == th && th.turnCtx == ctx
			if current {
				pauseAutonomyLocked(th)
				s.restoreScheduledSessionLocked(entry, th, turn)
				th.running = false
				th.turnHistory = nil
				th.cancel = nil
				th.turnCtx = nil
				th.workingTool = ""
				denyApprovalsLocked(th)
			}
			s.agentSessionsMu.Unlock()
			cancel()
			if current {
				s.agentBroadcast(entry, agentMsg{Op: "error", Thread: th.id, Message: fmt.Sprintf("internal error: %v", r)})
				s.persistAutonomy(entry, th)
				s.publishAutonomy(entry, th, "Agent error paused automatic continuation")
			}
		}
	}()

	var (
		doneUsage     *agentUsage
		doneReasoning string
	)
	emit := func(e agent.Event) {
		if turn.scheduled != nil {
			turn.scheduled.emit(e)
		}
		if s.emitAgentTurnEvent(entry, th, startEpoch, e) && e.Type == agent.EventDone {
			doneUsage = &agentUsage{InputTokens: e.Usage.InputTokens, OutputTokens: e.Usage.OutputTokens}
			doneReasoning = e.Reasoning
		}
	}

	// Record admission before execution, without holding the global session
	// lock during disk I/O. Reset/delete share the thread persistence lock.
	th.persistMu.Lock()
	s.agentSessionsMu.Lock()
	admitted := th.epoch == startEpoch && entry.threads[th.id] == th
	s.agentSessionsMu.Unlock()
	if admitted {
		if store := s.agentStore(); store != nil {
			store.saveOutcome(entry.deviceID, th.id, turnModel, "running", "")
		}
	}
	th.persistMu.Unlock()
	if !admitted {
		cancel()
		return
	}

	takeSteering := func() *agent.Message {
		if turn.scheduled != nil {
			return nil
		}
		s.agentSessionsMu.Lock()
		if ctx.Err() != nil || th.epoch != startEpoch || entry.threads[th.id] != th || len(th.queue) == 0 || !th.queue[0].steering {
			s.agentSessionsMu.Unlock()
			return nil
		}
		next := th.queue[0]
		// Slash commands need their own dispatcher/turn lifecycle.
		if strings.HasPrefix(next.text, "/") {
			s.agentSessionsMu.Unlock()
			return nil
		}
		takeQueuedChat(&th.queue, 0)
		th.goalActivity = ""
		s.agentSessionsMu.Unlock()
		s.broadcastQueue(entry, th)
		display, _ := json.Marshal(map[string]any{"steering": true, "time": time.Now().UnixMilli()})
		return &agent.Message{Role: "user", Content: next.text, Images: next.images, Display: display}
	}
	revision := sess.HistoryRevision()
	explicitCompaction, runErr := runAgentConversation(ctx, sess, text, images, emit, takeSteering)
	historyReplaced := explicitCompaction || sess.HistoryRevision() != revision
	if command, _ := splitAgentCommand(text); !isAgentSessionCommand(command) {
		turn.historyIndex = sess.TurnHistoryIndex()
	}
	cleanupAutomation()
	activity := ""
	if turn.autonomy == "goal" {
		history := sess.History()
		if turn.historyIndex >= 0 && turn.historyIndex < len(history) {
			activity = goalTurnActivity(history[turn.historyIndex:])
		}
	}
	outcomeAt := time.Now().UTC().Format(time.RFC3339Nano)
	outcome := "done"
	if runErr != nil {
		if ctx.Err() == context.Canceled {
			outcome = "stopped"
		} else {
			outcome = "error"
		}
	}
	timing, _ := json.Marshal(map[string]int64{"time": turn.started, "endedAt": time.Now().UnixMilli()})
	if !explicitCompaction && turn.historyIndex >= 0 {
		sess.AnnotateTurn(turn.historyIndex, turn.display, timing)
	}

	// Serialize history with reset/delete for this thread, leaving other
	// conversations and streaming/control messages free during slow storage.
	th.persistMu.Lock()
	s.agentSessionsMu.Lock()
	valid := th.epoch == startEpoch && entry.threads[th.id] == th
	s.agentSessionsMu.Unlock()
	if valid {
		if store := s.agentStore(); store != nil {
			if historyReplaced {
				store.replace(entry.deviceID, th.id, turnModel, sess.History())
			} else {
				store.save(entry.deviceID, th.id, turnModel, sess.History())
			}
			store.saveOutcome(entry.deviceID, th.id, turnModel, outcome, outcomeAt)
		}
	}
	// Remain running until persistence completes.
	s.agentSessionsMu.Lock()
	// Reset/delete may have admitted a replacement while this turn unwound.
	// Its state, approvals and queue belong to that replacement, so an old
	// completion must not clear or drain them (nor publish a terminal frame).
	if th.epoch != startEpoch || entry.threads[th.id] != th || th.turnCtx != ctx {
		s.agentSessionsMu.Unlock()
		th.persistMu.Unlock()
		cancel()
		return
	}
	finishAutonomyLocked(th, turn, outcome, activity)
	if turn.scheduled != nil {
		turn.scheduled.finish(ctx, runErr)
		s.restoreScheduledSessionLocked(entry, th, turn)
	}
	var terminal agentMsg
	switch outcome {
	case "done":
		terminal = agentMsg{
			Op:        "done",
			Thread:    th.id,
			Model:     turnModel,
			Usage:     doneUsage,
			Reasoning: doneReasoning,
			Files:     append([]string(nil), th.turnFiles...),
			Cwd:       agentEffectiveCwdLocked(entry, th),
			Branch:    th.branch,
		}
	case "stopped":
		terminal = agentMsg{Op: "stopped", Thread: th.id, Canceled: true}
	default:
		terminal = agentMsg{Op: "error", Thread: th.id, Message: runErr.Error()}
		if errors.Is(runErr, agent.ErrContentFiltered) {
			terminal.Code = "provider_content_filter"
		}
	}
	if historyReplaced {
		terminal.Compacted = true
		terminal.History = renderAgentHistory(sess.History())
	}
	if turn.rpc != nil {
		rpcErr = runErr
		if runErr == nil && !explicitCompaction {
			rpcData = map[string]any{"thread": th.id, "status": "completed"}
		}
		if explicitCompaction && runErr == nil {
			history := sess.History()
			// rdev has no persistent OMP entry IDs or pre-compaction token
			// estimate; do not fabricate those fields.
			rpcData = map[string]any{"summary": strings.TrimPrefix(history[0].Content, "Context from earlier conversation (summary):\n\n")}
		}
	}
	terminal.Display = timing
	var terminalData []byte
	th.lastOutcome = terminal.Op
	th.outcomeAt = outcomeAt
	if data, err := json.Marshal(terminal); err == nil {
		terminalData = data
		th.live.Append(terminal, data)
	}
	th.running = false
	th.turnHistory = nil
	th.cancel = nil
	th.turnCtx = nil
	th.workingTool = ""
	th.updatedAt = time.Now()
	denyApprovalsLocked(th)

	turnFiles := th.turnFiles
	var next agentQueuedChat
	var nextFrame agentMsg
	var nextTurn agentTurnSnapshot
	hasNext := queueHeadReady(entry, th, time.Now())
	prefsDirty := false
	if hasNext {
		next = takeQueuedChat(&th.queue, 0)
		prefsDirty = s.applyQueuedModelLocked(entry, th, next)
		nextFrame = s.startAgentTurnLocked(entry, th, next.text, next.images, next.display)
		nextTurn = snapshotAgentTurnLocked(th)
	}
	s.agentSessionsMu.Unlock()
	th.persistMu.Unlock()
	cancel()

	s.persistAutonomy(entry, th)
	s.publishAutonomy(entry, th, "")
	if terminalData != nil {
		s.agentBroadcastData(entry, terminalData)
	}
	if prefsDirty {
		s.writeAgentPrefs(entry)
	}
	s.broadcastThreadList(entry)
	if len(turnFiles) > 0 {
		s.scheduleThreadMetaProbe(entry, th.id)
	}
	if hasNext {
		s.broadcastQueue(entry, th)
		s.agentBroadcastTurn(entry, th, nextFrame)
		s.goAgent(func() { s.runAgentTurn(entry, th, nextTurn, next.text, next.images) })
	}
	if !hasNext {
		s.scheduleAutonomy(entry, th)
	}
}

// HandleAgentWS handles browser agent WebSocket connections on /agent?device=<id>.
func (s *Server) HandleAgentWS(w http.ResponseWriter, r *http.Request) {
	if !s.requireAuth(w, r) {
		return
	}
	if !requireOrigin(w, r) {
		return
	}
	deviceID := r.URL.Query().Get("device")
	if deviceID == "" {
		http.Error(w, "missing device parameter", http.StatusBadRequest)
		return
	}

	upgrader := gws.NewUpgrader(&agentWSHandler{srv: s, adminBypass: s.deviceAuthBypassed(r)}, &gws.ServerOption{
		// Chat frames may carry up to agentMaxImages base64 images of
		// agentMaxImageBytes each (~22MB), plus envelope slack.
		ReadMaxPayloadSize: 24 * 1024 * 1024,
		ParallelGolimit:    1,
		Authorize: func(r *http.Request, session gws.SessionStorage) bool {
			session.Store("deviceID", deviceID)
			return true
		},
		PermessageDeflate: gws.PermessageDeflate{Enabled: false},
	})

	socket, err := upgrader.Upgrade(w, r)
	if err != nil {
		log.Printf("agent ws upgrade error: %v", err)
		return
	}
	socket.ReadLoop()
}

const agentDesktopGuidance = `

Desktop capability and execution:
- Tool definitions and fresh results are authoritative for capabilities; old assistant claims and summaries may be wrong. Before declaring desktop/CUA unsupported, check computer status on the intended online device. Distinguish unsupported capability from offline transport, missing permission, and another conversation's control lease. A failed call does not prove the entire driver is unavailable.
- When cua=true, read skill cua-driver/SKILL.md and WORKFLOW.md. Discover the compact catalog with cua action=tools, then request action=tools with tool=<exact name> for the needed schema before calling it. Prefer semantic/accessibility actions. RDev owns the embedded session: do not invent create_session, supply session IDs, or install a CLI/cloud service to use it.
- Screenshots are optional for native CUA calls and computer input. Use screenshots when visual context or pixel coordinates are needed, and verify meaningful actions with fresh observations. Follow each native tool's coordinate space; computer screenshot pixels are not automatically native screen points. Never blindly retry uncertain input or switch backends to bypass permission/takeover.
- An agent cursor overlay is not the system pointer. Follow the skill's enable/place/verify workflow; a successful call or enabled=true is not visual proof. Turn completion and desktop cleanup end the session and its overlay; do not promise a persistent cursor.
- The browser tool uses a separate server-hosted browser. Use computer/CUA for the device's installed browser, existing tabs or logged-in desktop session. Do not silently substitute one environment for the other.
- Complete authorized, bounded actions without asking the user to reconfirm them. Ask only for unresolved material choices or additional consequential actions. Report observed results, uncertainty and exact blockers; never claim a fix, visibility or deployment without evidence.`

func buildAgentSystemPrompt(deviceID, cwd string) string {
	if deviceID == agentServerDeviceID {
		return "You are the server agent in rdev. The server is your coordination environment only. No server-host shell or filesystem access is available. Call list_devices to discover currently connected remote devices and their tool schemas, then device_tool with an explicit device ID. Use agent_sessions to list or read Agent conversations (including offline history), and manage_agent_session to create, rename or delete a specified conversation; discover exact device and thread IDs before managing them. No device registration is needed. Check the live device list rather than inventing device IDs. Use subagent for isolated delegated tasks, jobs to inspect or cancel them, and verify their results. Work autonomously within these capabilities and answer in the user's language." + agentDesktopGuidance
	}
	prompt := `You are an agent inside rdev, a remote-device management server. Device shell and file tools operate on the device identified below, not on the server or the user's machine. Other tools run where their descriptions specify; never assume a shared filesystem, browser session or authentication context.

Tools:
- shell: run a shell command. Prefer it for discovery (uname, ls, cat small files, system info).
- bg: start a device shell command in the background; completion is automatically delivered.
- jobs: list/read/wait/cancel background work. Waiting does not cancel a job.
- subagent: delegate a self-contained task with an explicit subset of tools; children report back automatically. Verify their claimed results.
- list_dir: list a directory.
- read_file / write_file: read/write file contents. read_file pages long files with offset/limit; with anchors=true every line gets an N#hash anchor.
- edit: modify an existing file. Prefer line_edits mode (hashline anchors from read_file anchors=true — cheaper and verified) over old_string for targeted changes; stale anchors are rejected, just re-read and retry.
- grep / glob: search file contents / find files by name on the device.
- web_search / web_fetch: search the web and read pages (run on the server, not the device).
- ask: when the user's request is ambiguous or a decision is needed mid-task, ask a structured question with options instead of guessing.
- todo_write: plan multi-step tasks first and keep the checklist updated as you make progress.

Guidelines:
- Work autonomously: gather what you need with tools before answering.
- Before destructive commands (rm -rf, dd, mkfs, dropping data, killing services), briefly state the risk, then still execute what the user asked.
- Quote paths carefully; the device may be any OS/arch.
- Answer in the user's language, concisely. Report what you did and the outcome.`
	prompt += agentDesktopGuidance
	prompt += fmt.Sprintf("\n\nDevice context: shell and file tools operate on device %q.", deviceID)
	if cwd != "" {
		prompt += fmt.Sprintf("\n\nCurrent project directory: %s (relative paths in tool calls are resolved against it).", cwd)
	}
	return prompt
}

// agentEffectiveCwdLocked returns a thread's working directory: its own cwd
// when set, else the device default. Caller must hold agentSessionsMu.
func agentEffectiveCwdLocked(entry *agentSessionEntry, th *agentThread) string {
	if th.cwd != "" {
		return th.cwd
	}
	return entry.defaultCwd
}

// scheduleThreadMetaProbe probes a thread's git branch in the background
// (no locks held while the device round-trip runs) and broadcasts the
// thread_meta snapshot once done, followed by a changes push with the
// working-tree status.
func (s *Server) scheduleThreadMetaProbe(entry *agentSessionEntry, threadID string) {
	s.goAgent(func() {
		s.agentSessionsMu.Lock()
		initial := entry.threads[threadID]
		needsDefault := initial != nil && agentEffectiveCwdLocked(entry, initial) == ""
		s.agentSessionsMu.Unlock()
		if needsDefault {
			s.awaitAgentDefaultCwd(entry)
		}
		s.agentSessionsMu.Lock()
		th := entry.threads[threadID]
		if th == nil {
			s.agentSessionsMu.Unlock()
			return
		}
		cwd := agentEffectiveCwdLocked(entry, th)
		th.metaProbeSeq++
		probeSeq := th.metaProbeSeq
		s.agentSessionsMu.Unlock()

		branch := ""
		if cwd != "" {
			branch = s.probeGitBranch(entry.deviceID, cwd)
		}

		s.agentSessionsMu.Lock()
		th = entry.threads[threadID]
		if th == nil || th.metaProbeSeq != probeSeq || agentEffectiveCwdLocked(entry, th) != cwd {
			s.agentSessionsMu.Unlock()
			return
		}
		th.branch = branch
		s.agentSessionsMu.Unlock()
		s.agentBroadcast(entry, agentMsg{Op: "thread_meta", Thread: threadID, Cwd: cwd, Branch: branch})

		// Push the working-tree status along with every meta refresh (turn
		// done, set_cwd, switch_thread, auth_ok) so the changes panel stays
		// current without polling.
		changes := agentChangesMsg{Op: "changes", Thread: threadID, Cwd: cwd, Branch: branch, Files: []agentChangeFile{}}
		if cwd == "" {
			changes.Error = "no working directory"
		} else if files, err := s.probeGitChanges(entry.deviceID, cwd); err != nil {
			changes.Error = err.Error()
		} else {
			changes.Files = files
		}
		s.agentSessionsMu.Lock()
		th = entry.threads[threadID]
		current := th != nil && th.metaProbeSeq == probeSeq && agentEffectiveCwdLocked(entry, th) == cwd
		s.agentSessionsMu.Unlock()
		if !current {
			return
		}
		if data, err := json.Marshal(changes); err == nil {
			s.agentBroadcastData(entry, data)
		}
	})
}

// probeAgentDefaultCwd discovers the device client's own working directory
// (pwd / cd) once per entry and backfills thread_meta for the current
// thread. Runs off-lock; failures leave defaultCwd empty.
func (s *Server) probeAgentDefaultCwd(entry *agentSessionEntry) {
	if s.awaitAgentDefaultCwd(entry) == "" {
		return
	}
	s.agentSessionsMu.Lock()
	current := entry.current
	s.agentSessionsMu.Unlock()
	s.scheduleThreadMetaProbe(entry, current)
}

// awaitAgentDefaultCwd runs off-lock and shares one bounded probe among cold
// panel requests. Failures are not cached, so reconnects can retry without a
// model turn. A failed command's stdout must never become a working directory.
func (s *Server) awaitAgentDefaultCwd(entry *agentSessionEntry) string {
	s.agentSessionsMu.Lock()
	if entry.defaultCwd != "" || entry.deviceID == agentServerDeviceID {
		cwd := entry.defaultCwd
		s.agentSessionsMu.Unlock()
		return cwd
	}
	if pending := entry.defaultCwdProbe; pending != nil {
		s.agentSessionsMu.Unlock()
		<-pending
		s.agentSessionsMu.Lock()
		cwd := entry.defaultCwd
		s.agentSessionsMu.Unlock()
		return cwd
	}
	pending := make(chan struct{})
	entry.defaultCwdProbe = pending
	s.agentSessionsMu.Unlock()
	cwd := ""
	if client, ok := s.GetClient(entry.deviceID); ok {
		command := "pwd"
		if clientOS(client) == "windows" {
			command = "cd"
		}
		out, code, err := s.runDeviceProbe(client, command, 5*time.Second, 4096)
		if err == nil && code == 0 {
			cwd, _, _ = strings.Cut(out, "\n")
			cwd = strings.TrimSpace(cwd)
		}
	}
	s.agentSessionsMu.Lock()
	if entry.defaultCwd == "" {
		entry.defaultCwd = cwd
	}
	cwd = entry.defaultCwd
	entry.defaultCwdProbe = nil
	close(pending)
	s.agentSessionsMu.Unlock()
	return cwd
}

// probeGitBranch asks a device for the current git branch of cwd; "" when
// offline, not a repo, or the probe fails. The client connection is always
// fetched fresh (entry.client goes stale on device reconnects).
func (s *Server) probeGitBranch(deviceID, cwd string) string {
	client, ok := s.GetClient(deviceID)
	if !ok || cwd == "" {
		return ""
	}
	out, _, err := s.runDeviceProbe(client, gitBranchProbeCommand(clientOS(client), cwd), agentGitProbeTimeout, 4096)
	if err != nil {
		return ""
	}
	branch, _, _ := strings.Cut(out, "\n")
	return strings.TrimSpace(branch)
}

// Git probe tuning: branch/status probes are quick, diffs may be large and
// are capped (truncation marker included by cappedBuffer).
const (
	agentGitProbeTimeout = 5 * time.Second
	agentGitDiffTimeout  = 30 * time.Second
	agentGitStatusCap    = 64 * 1024
	agentGitDiffCap      = 200 * 1024
)

// errGitNotRepo marks "cwd is not a git repository" (or the status/diff
// command failed); errGitUntracked marks a diff requested for a file git
// does not track.
var (
	errGitNotRepo   = errors.New("not a git repository")
	errGitUntracked = errors.New("untracked")
)

// probeGitChanges asks a device for the working-tree status of cwd and
// parses the porcelain output. The client connection is always fetched
// fresh (entry.client goes stale on device reconnects); runs off-lock.
func (s *Server) probeGitChanges(deviceID, cwd string) ([]agentChangeFile, error) {
	if cwd == "" {
		return nil, fmt.Errorf("no working directory")
	}
	client, ok := s.GetClient(deviceID)
	if !ok {
		return nil, fmt.Errorf("device offline")
	}
	goos := clientOS(client)
	out, code, err := s.runDeviceProbe(client, gitStatusProbeCommand(goos, cwd), agentGitProbeTimeout, agentGitStatusCap)
	if err != nil {
		return nil, err
	}
	// code -1 means the device never reported one (older client); keep the
	// soft-failure behavior and parse whatever stdout produced.
	if code > 0 {
		return nil, errGitNotRepo
	}
	files := parseGitStatusPorcelain(out)
	// Line counts ride along via a numstat probe (HEAD, falling back to the
	// unstaged diff when the repo has no commits yet). Failure is soft: the
	// panel keeps working, just without the +/- tallies.
	if ns, nsCode, nsErr := s.runDeviceProbe(client, gitNumstatProbeCommand(goos, cwd, true), agentGitProbeTimeout, agentGitStatusCap); nsErr == nil {
		if nsCode > 0 {
			// No HEAD yet (fresh repo): count the unstaged diff instead.
			ns, _, _ = s.runDeviceProbe(client, gitNumstatProbeCommand(goos, cwd, false), agentGitProbeTimeout, agentGitStatusCap)
		}
		mergeGitNumstat(files, parseGitNumstat(ns))
	}
	return files, nil
}

// probeGitDiff asks a device for the unified diff of path in cwd. Repos
// without commits have no HEAD, so the HEAD diff falls back to a plain
// unstaged diff; a path with no diff output at all is checked against the
// index to tell untracked files (error "untracked") from unchanged ones
// (empty diff).
func (s *Server) probeGitDiff(deviceID, cwd, path string) (string, error) {
	if cwd == "" || path == "" {
		return "", fmt.Errorf("missing cwd or path")
	}
	client, ok := s.GetClient(deviceID)
	if !ok {
		return "", fmt.Errorf("device offline")
	}
	goos := clientOS(client)
	out, code, err := s.runDeviceProbe(client, gitDiffProbeCommand(goos, cwd, path, true), agentGitDiffTimeout, agentGitDiffCap)
	if err != nil {
		return "", err
	}
	if code > 0 {
		out, code, err = s.runDeviceProbe(client, gitDiffProbeCommand(goos, cwd, path, false), agentGitDiffTimeout, agentGitDiffCap)
		if err != nil {
			return "", err
		}
		if code > 0 {
			return "", errGitNotRepo
		}
	}
	if out == "" {
		_, lsCode, lsErr := s.runDeviceProbe(client, gitLsFilesProbeCommand(goos, cwd, path), agentGitProbeTimeout, 4096)
		if lsErr == nil && lsCode > 0 {
			return "", errGitUntracked
		}
	}
	return out, nil
}

// File read probe tuning: previews are capped at 256 KiB; the probe asks for
// one extra byte to detect truncation.
const (
	agentFileReadCap     = 256 * 1024
	agentFileReadTimeout = 15 * time.Second
)

// probeReadFile reads a file on a device (relative paths resolve against
// cwd) and returns its content plus a truncation flag. Binary files (NUL in
// the first 8 KiB) are rejected. Runs off-lock.
func (s *Server) probeReadFile(deviceID, cwd, path string) (string, bool, error) {
	if cwd == "" || path == "" {
		return "", false, fmt.Errorf("missing cwd or path")
	}
	client, ok := s.GetClient(deviceID)
	if !ok {
		return "", false, fmt.Errorf("device offline")
	}
	out, code, err := s.runDeviceProbe(client, fileReadProbeCommand(clientOS(client), cwd, path, agentFileReadCap), agentFileReadTimeout, agentFileReadCap+1024)
	if err != nil {
		return "", false, err
	}
	if code > 0 {
		return "", false, fmt.Errorf("cannot read %s", path)
	}
	truncated := len(out) > agentFileReadCap
	if truncated {
		out = out[:agentFileReadCap]
	}
	probe := out
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	if strings.IndexByte(probe, 0) >= 0 {
		return "", false, fmt.Errorf("binary file")
	}
	return out, truncated, nil
}

// fileReadProbeCommand builds the read probe for the device OS, emitting at
// most cap+1 bytes so the caller can tell truncation apart from an exact
// fit. POSIX uses head(1); Windows uses PowerShell byte reads.
func fileReadProbeCommand(goos, cwd, path string, cap int) string {
	if goos == "windows" {
		q := func(s string) string { return strings.ReplaceAll(s, `'`, `''`) }
		return fmt.Sprintf(`powershell -NoProfile -Command "Set-Location -LiteralPath '%s'; $b=[IO.File]::ReadAllBytes('%s'); $n=[Math]::Min($b.Length,%d); $s=[Console]::OpenStandardOutput(); $s.Write($b,0,$n)"`, q(cwd), q(path), cap+1)
	}
	return "cd " + shellQuote(cwd) + " && head -c " + strconv.Itoa(cap+1) + " -- " + shellQuote(path)
}

// Dir listing probe tuning: at most 2000 entries per directory, 15s.
const (
	agentDirListMaxEntries = 2000
	agentDirListTimeout    = 15 * time.Second
)

// probeListDir lists one directory on a device: child entries (dirs first
// ordering is the caller's concern), the parent path, and filesystem roots
// (["/"] on POSIX, drive letters on Windows). Runs off-lock.
func (s *Server) probeListDir(deviceID, path string) (string, []agentDirEntry, []string, error) {
	client, ok := s.GetClient(deviceID)
	if !ok {
		return "", nil, nil, fmt.Errorf("device offline")
	}
	out, code, err := s.runDeviceProbe(client, dirListProbeCommand(clientOS(client), path), agentDirListTimeout, agentFileReadCap+1024)
	if err != nil {
		return "", nil, nil, err
	}
	if code > 0 {
		return "", nil, nil, fmt.Errorf("cannot read %s", path)
	}
	parent := ""
	entries := []agentDirEntry{}
	roots := []string{}
	sep := "/"
	if clientOS(client) == "windows" {
		sep = `\`
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "__RDEV_PARENT__"):
			parent = strings.TrimPrefix(line, "__RDEV_PARENT__")
		case strings.HasPrefix(line, "__RDEV_ROOT__"):
			roots = append(roots, strings.TrimPrefix(line, "__RDEV_ROOT__"))
		case len(line) > 2 && (strings.HasPrefix(line, "D;") || strings.HasPrefix(line, "F;")):
			name := line[2:]
			if name == "" {
				continue
			}
			entries = append(entries, agentDirEntry{
				Name: name,
				Path: strings.TrimSuffix(path, sep) + sep + name,
				Dir:  line[0] == 'D',
			})
		}
	}
	if parent == "" && len(entries) == 0 && len(roots) == 0 {
		return "", nil, nil, fmt.Errorf("cannot read %s", path)
	}
	if len(roots) == 0 && sep == "/" {
		roots = []string{"/"}
	}
	return parent, entries, roots, nil
}

// dirListProbeCommand builds the listing probe for the device OS. Output
// protocol: a __RDEV_PARENT__ line, one "D;name"/"F;name" line per entry
// (parsed on the first ";" only, so names may contain ";"), and
// __RDEV_ROOT__ lines for Windows drives. Non-directory → non-zero exit.
func dirListProbeCommand(goos, path string) string {
	if goos == "windows" {
		q := strings.ReplaceAll(path, `'`, `''`)
		return fmt.Sprintf(`powershell -NoProfile -Command "$p='%s'; if(-not (Test-Path -LiteralPath $p -PathType Container)){ exit 1 }; Write-Output ('__RDEV_PARENT__' + (Split-Path -LiteralPath $p -Parent)); Get-PSDrive -PSProvider FileSystem | ForEach-Object { Write-Output ('__RDEV_ROOT__' + $_.Name + ':\') }; Get-ChildItem -LiteralPath $p -Force -Name | Select-Object -First %d | ForEach-Object { if(Test-Path -LiteralPath (Join-Path $p $_) -PathType Container){ Write-Output ('D;' + $_) } else { Write-Output ('F;' + $_) } }"`, q, agentDirListMaxEntries)
	}
	return "d=" + shellQuote(path) + ` && [ -d "$d" ] && { echo "__RDEV_PARENT__$(dirname -- "$d")"; ls -1Ap -- "$d" 2>/dev/null | head -n ` + strconv.Itoa(agentDirListMaxEntries) + ` | while IFS= read -r e; do case "$e" in */) printf 'D;%s\n' "${e%/}";; *) printf 'F;%s\n' "$e";; esac; done; }`
}

// probeMkdir creates a directory (with parents) on a device. Runs off-lock.
func (s *Server) probeMkdir(deviceID, path string) error {
	client, ok := s.GetClient(deviceID)
	if !ok {
		return fmt.Errorf("device offline")
	}
	_, code, err := s.runDeviceProbe(client, mkdirProbeCommand(clientOS(client), path), agentDirListTimeout, 4096)
	if err != nil {
		return err
	}
	if code > 0 {
		return fmt.Errorf("cannot create %s", path)
	}
	return nil
}

// mkdirProbeCommand builds the mkdir probe for the device OS: POSIX mkdir
// -p, Windows PowerShell New-Item -Force.
func mkdirProbeCommand(goos, path string) string {
	if goos == "windows" {
		q := strings.ReplaceAll(path, `'`, `''`)
		return fmt.Sprintf(`powershell -NoProfile -Command "New-Item -ItemType Directory -Force -Path '%s' | Out-Null"`, q)
	}
	return "mkdir -p -- " + shellQuote(path)
}

// parseGitStatusPorcelain parses `git status --porcelain=v1 -uall` output
// into path/status pairs. Rename/copy entries ("R  old -> new") report the
// new path; C-quoted paths are unquoted. Malformed lines are skipped.
func parseGitStatusPorcelain(out string) []agentChangeFile {
	files := []agentChangeFile{}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 || line[2] != ' ' {
			continue
		}
		status := porcelainStatusLetter(line[0], line[1])
		if status == "" {
			continue
		}
		path := line[3:]
		if status == "R" || status == "C" {
			if i := strings.LastIndex(path, " -> "); i >= 0 {
				path = path[i+len(" -> "):]
			}
		}
		files = append(files, agentChangeFile{Path: unquoteGitPath(path), Status: status})
	}
	return files
}

// porcelainStatusLetter reduces the two porcelain columns to one status
// letter: "??" for untracked, "" for ignored/blank, else the index column
// when set, falling back to the worktree column (" M" → "M").
func porcelainStatusLetter(x, y byte) string {
	if x == '?' && y == '?' {
		return "??"
	}
	if x == '!' || y == '!' {
		return ""
	}
	c := x
	if c == ' ' {
		c = y
	}
	if c == ' ' {
		return ""
	}
	return string(c)
}

// unquoteGitPath undoes the C-style quoting git applies to paths with
// special characters in porcelain output.
func unquoteGitPath(p string) string {
	if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
		if unquoted, err := strconv.Unquote(p); err == nil {
			return unquoted
		}
	}
	return p
}

// gitProbeQuote quotes one argument for the device shell: POSIX single
// quotes, or double quotes with \" escaping on Windows cmd.
func gitProbeQuote(goos, s string) string {
	if goos == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return shellQuote(s)
}

// gitBranchProbeCommand builds the branch-probe command for the device OS,
// quoting cwd for the respective shell.
func gitBranchProbeCommand(goos, cwd string) string {
	return "git -C " + gitProbeQuote(goos, cwd) + " branch --show-current"
}

// gitStatusProbeCommand builds the working-tree status probe.
func gitStatusProbeCommand(goos, cwd string) string {
	return "git -C " + gitProbeQuote(goos, cwd) + " status --porcelain=v1 -uall"
}

// gitNumstatProbeCommand builds the whole-tree numstat probe, against HEAD
// when withHead is set, else unstaged-only.
func gitNumstatProbeCommand(goos, cwd string, withHead bool) string {
	cmd := "git -C " + gitProbeQuote(goos, cwd) + " diff --numstat"
	if withHead {
		cmd += " HEAD"
	}
	return cmd + " --"
}

// parseGitNumstat parses `git diff --numstat` output into per-path line
// counts. Binary files ("-\t-\tpath") yield zeros. Rename compaction
// ("a/{old => new}/b", "old => new") resolves to the new path so counts
// match the porcelain path; C-quoted paths are unquoted. Malformed lines
// are skipped.
func parseGitNumstat(out string) map[string][2]int {
	counts := make(map[string][2]int)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		adds, errA := strconv.Atoi(fields[0])
		dels, errD := strconv.Atoi(fields[1])
		if errA != nil || errD != nil {
			continue // binary or malformed
		}
		counts[numstatNewPath(fields[2])] = [2]int{adds, dels}
	}
	return counts
}

// numstatNewPath extracts the post-rename path from numstat's path field,
// undoing git's brace compaction and C-quoting.
func numstatNewPath(p string) string {
	if i := strings.Index(p, " => "); i >= 0 {
		if open := strings.LastIndex(p[:i], "{"); open >= 0 {
			if close := strings.Index(p[i:], "}"); close >= 0 {
				// "a/{old => new}/b" → "a/" + "new" + "/b"
				p = p[:open] + p[i+len(" => "):i+close] + p[i+close+1:]
			}
		} else {
			// "old => new" → "new"
			p = p[i+len(" => "):]
		}
	}
	return unquoteGitPath(p)
}

// mergeGitNumstat fills Adds/Dels on each changed file from the numstat
// counts; paths missing from the map (untracked files) stay zero.
func mergeGitNumstat(files []agentChangeFile, counts map[string][2]int) {
	for i := range files {
		if c, ok := counts[files[i].Path]; ok {
			files[i].Adds = c[0]
			files[i].Dels = c[1]
		}
	}
}

// gitDiffProbeCommand builds the diff probe for one path, against HEAD when
// withHead is set, else unstaged-only.
func gitDiffProbeCommand(goos, cwd, path string, withHead bool) string {
	cmd := "git -C " + gitProbeQuote(goos, cwd) + " diff"
	if withHead {
		cmd += " HEAD"
	}
	return cmd + " -- " + gitProbeQuote(goos, path)
}

// gitLsFilesAllProbeCommand builds the file listing probe used by
// search_files: tracked files plus untracked-but-not-ignored files.
func gitLsFilesAllProbeCommand(goos, cwd string) string {
	return "git -C " + gitProbeQuote(goos, cwd) + " ls-files --cached --others --exclude-standard"
}

// gitLsFilesProbeCommand builds the tracked-file check for one path (exit
// code 0 = tracked).
func gitLsFilesProbeCommand(goos, cwd, path string) string {
	return "git -C " + gitProbeQuote(goos, cwd) + " ls-files --error-unmatch -- " + gitProbeQuote(goos, path)
}

// runDeviceProbe executes a short non-PTY command on a device and returns
// its stdout (capped) and exit code (-1 when the device never reported
// one). Soft-failure helper for cwd/branch/status/diff probing.
func (s *Server) runDeviceProbe(client *ClientConn, command string, timeout time.Duration, outputCap int) (string, int, error) {
	sessionID := generateID()
	sess := &ProxySession{
		ID:       sessionID,
		ClientID: client.ID,
		WriteCh:  make(chan []byte, 8192),
		StderrCh: make(chan []byte, 2048),
		CloseCh:  make(chan struct{}, 1),
		Done:     make(chan struct{}),
		exitDone: make(chan struct{}),
		CloseSSH: func() {},
		ExitSSH:  func(code int) {},
	}
	sess.SetSessionMeta(false, "", command, "", 0, 0)
	if !s.RegisterSession(sess, client) {
		return "", -1, fmt.Errorf("too many active sessions on device")
	}
	defer func() {
		s.removeSession(sessionID)
		client.mu.Lock()
		delete(client.Sessions, sessionID)
		client.mu.Unlock()
	}()

	if err := client.Send(&protocol.Message{
		Type:      protocol.MsgNewSession,
		ClientID:  client.ID,
		SessionID: sessionID,
		Command:   command,
		Pty:       false,
	}); err != nil {
		return "", -1, fmt.Errorf("failed to reach device: %v", err)
	}

	stdout := &cappedBuffer{cap: outputCap}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case data, ok := <-sess.WriteCh:
			if !ok {
				continue
			}
			stdout.Write(data)
		case <-sess.StderrCh: // drain
		case <-sess.CloseCh:
			sess.NotifyObserversClose()
			sess.CloseOutput()
			// The device reports the exit code just before closing; wait
			// briefly for the message to arrive.
			return stdout.String(), sess.WaitExitCode(500 * time.Millisecond), nil
		case <-timer.C:
			_ = client.Send(&protocol.Message{Type: protocol.MsgClose, SessionID: sessionID})
			select {
			case <-sess.CloseCh:
				sess.NotifyObserversClose()
				sess.CloseOutput()
			case <-time.After(500 * time.Millisecond):
			}
			return "", -1, fmt.Errorf("probe timed out after %s", timeout)
		}
	}
}
