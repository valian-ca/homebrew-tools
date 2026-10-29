package proto

import "time"

const (
	OpPing    = "ping"
	OpStart   = "start"
	OpStop    = "stop"
	OpUp      = "up"
	OpRestart = "restart"
	OpNotify  = "notify"
	OpList    = "list"
)

type Request struct {
	Op       string `json:"op"`
	Worktree string `json:"worktree,omitempty"`
	Proc     string `json:"proc,omitempty"`
	// Environ is the caller's environment: the stack runs with the agent's
	// PATH and tools, not with the bare environment launchd gives lanesd.
	Environ []string `json:"environ,omitempty"`
	// Binary is process-compose as the caller's PATH resolves it.
	Binary string `json:"binary,omitempty"`
}

type Response struct {
	OK             bool          `json:"ok"`
	Error          string        `json:"error,omitempty"`
	AlreadyRunning bool          `json:"alreadyRunning,omitempty"`
	NoSession      bool          `json:"noSession,omitempty"`
	Stacks         []StackStatus `json:"stacks,omitempty"`
}

type StackStatus struct {
	AdminDir   string     `json:"adminDir"`
	Running    bool       `json:"running"`
	Sessions   int        `json:"sessions"`
	GraceUntil *time.Time `json:"graceUntil,omitempty"`
}
