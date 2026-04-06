package evaluations

import "context"

// AgentAdapter runs the agent-specific middle of one trial: project instruction
// staging, seed bootstrap translation, process launch, native artifact
// collection, and adaptation into agent-neutral transcript events and command
// records.
//
// The harness remains responsible for trial root creation, workspace or
// in-place setup, temp HOME/XDG roots, mock-provider setup, diff/workspace
// capture, and grader execution.
type AgentAdapter interface {
	Run(ctx context.Context, req AdapterRequest) (AdapterResult, error)
}

// AdapterRequest carries everything an adapter needs to run one trial.
type AdapterRequest struct {
	TaskRoot     string
	Task         Task
	WorkspaceDir string
	HomeDir      string
	ConfigDir    string
	StateDir     string
	TrialRoot    string
	BinaryPath   string
	Env          []string
}

// AdapterResult carries what the adapter produced from one trial run.
type AdapterResult struct {
	ExitCode     int
	AgentVersion string
	AgentCommand []string
	SystemPrompt string

	// Trajectory and EventLog preserve clnku-native raw outputs for backward
	// compatibility. Future adapters populate only the generic fields below.
	Trajectory string
	EventLog   string

	// Agent-neutral adapted artifacts for normalization and grading.
	TranscriptEvents  []TranscriptEvent
	Commands          []CommandRecord
	RawAgentArtifacts []RawAgentArtifact
}

// TranscriptEvent is one visible adapted transcript event that normalization
// can consume without parsing agent-specific payloads.
type TranscriptEvent struct {
	Index    int    `json:"index"`
	Kind     string `json:"kind"`
	Role     string `json:"role,omitempty"`
	TurnType string `json:"turn_type,omitempty"`
	Content  string `json:"content,omitempty"`
	Command  string `json:"command,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
}

// CommandRecord captures one visible command execution for grading.
type CommandRecord struct {
	Command  string `json:"command"`
	Dir      string `json:"dir,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// RawAgentArtifact describes a named native file to persist under raw/agent/.
type RawAgentArtifact struct {
	Name    string `json:"name"`
	Content []byte `json:"-"`
}
