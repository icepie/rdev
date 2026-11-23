package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"rdev/internal/agent"

	"github.com/lxzan/gws"
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
	MessageID      string             `json:"messageId,omitempty"`
	Before         string             `json:"before,omitempty"`
	Around         string             `json:"around,omitempty"`
	Limit          int                `json:"limit,omitempty"`
	EditText       *string            `json:"editText,omitempty"`
	Draft          *string            `json:"draft,omitempty"`
	HistoryHasMore bool               `json:"historyHasMore"`
	HistoryBefore  string             `json:"historyBefore"`
	Hits           []agentHistoryHit  `json:"hits,omitempty"`
	Base           string             `json:"base,omitempty"`
	Goal           *agentGoal         `json:"goal"`
	Loop           *agentLoop         `json:"loop"`
	Action         string             `json:"action,omitempty"`
	Objective      string             `json:"objective,omitempty"`
	TokenBudget    *int64             `json:"tokenBudget,omitempty"`
	Participants   []agentParticipant `json:"participants,omitempty"`

	Automation         *automationView          `json:"automation,omitempty"`
	Pinned             bool                     `json:"pinned,omitempty"`
	Offline            bool                     `json:"offline,omitempty"`
	OfflineConfirmed   bool                     `json:"offlineConfirmed,omitempty"`
	Type               string                   `json:"type,omitempty"`
	Command            string                   `json:"command,omitempty"`
	Success            *bool                    `json:"success,omitempty"`
	Error              string                   `json:"error,omitempty"`
	Code               string                   `json:"code,omitempty"`
	Diagnostic         *agent.FailureDiagnostic `json:"diagnostic,omitempty"`
	Data               json.RawMessage          `json:"data,omitempty"`
	RPC                bool                     `json:"-"`
	Display            json.RawMessage          `json:"display,omitempty"`
	ImagePreviews      []string                 `json:"imagePreviews,omitempty"`
	Compacted          bool                     `json:"compacted,omitempty"`
	Op                 string                   `json:"op"`
	Password           string                   `json:"password,omitempty"`
	Text               string                   `json:"text,omitempty"`
	Device             string                   `json:"device,omitempty"`
	Model              string                   `json:"model,omitempty"`
	ModelName          string                   `json:"modelName,omitempty"`
	Models             []map[string]any         `json:"models,omitempty"`
	Message            string                   `json:"message,omitempty"`
	History            []agentHistoryEntry      `json:"history,omitempty"`
	Running            bool                     `json:"running,omitempty"`
	HistoryError       string                   `json:"historyError,omitempty"`
	ID                 string                   `json:"id,omitempty"`
	JobID              string                   `json:"jobId,omitempty"`
	Recovered          int                      `json:"recovered,omitempty"`
	SubagentID         string                   `json:"subagentId,omitempty"`
	CustomInstructions string                   `json:"customInstructions,omitempty"`
	FromByte           *int64                   `json:"fromByte,omitempty"`
	SessionFile        string                   `json:"sessionFile,omitempty"`
	Name               string                   `json:"name,omitempty"`
	Args               string                   `json:"args,omitempty"`
	Result             string                   `json:"result,omitempty"`
	IsError            bool                     `json:"isError,omitempty"`
	// Reason is a stable machine code for a UI-localised failure (queue ops).
	Reason     string            `json:"reason,omitempty"`
	Usage      *agentUsage       `json:"usage,omitempty"`
	Summary    string            `json:"summary,omitempty"`
	Diff       string            `json:"diff,omitempty"`
	Reasoning  string            `json:"reasoning,omitempty"`
	Thread     string            `json:"thread,omitempty"`
	Threads    []agentThreadInfo `json:"threads,omitempty"`
	Todos      []todoItem        `json:"todos,omitempty"`
	QueueModes []string          `json:"queueModes,omitempty"`
	QueueIDs   []string          `json:"queueIds,omitempty"`
	Queue      []string          `json:"queue,omitempty"`
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
	Cwd        string `json:"cwd,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Path       string `json:"path,omitempty"`
	Title      string `json:"title,omitempty"`
	State      string `json:"state,omitempty"`
	Repository string `json:"repository,omitempty"`
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
	PinnedAt       int64                    `json:"pinnedAt,omitempty"`
	OutcomeAt      string                   `json:"outcomeAt,omitempty"`
	Outcome        string                   `json:"outcome,omitempty"`
	Diagnostic     *agent.FailureDiagnostic `json:"diagnostic,omitempty"`
	BackgroundJobs int                      `json:"backgroundJobs,omitempty"`
	ID             string                   `json:"id"`
	Title          string                   `json:"title"`
	UpdatedAt      string                   `json:"updatedAt"`
	Running        bool                     `json:"running"`
	Waiting        string                   `json:"waiting,omitempty"`
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
