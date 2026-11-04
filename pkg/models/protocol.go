package models

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// MessageType identifies a message on the agent <-> server WebSocket.
type MessageType string

const (
	// MsgAgentHello is the first message an agent sends after connecting.
	MsgAgentHello MessageType = "agent_hello"
	// MsgJobAssigned is sent by the server to hand a job to an agent.
	MsgJobAssigned MessageType = "job_assigned"
	// MsgJobRejected is sent by an agent that cannot take an assigned job.
	MsgJobRejected MessageType = "job_rejected"
	// MsgJobLog carries a chunk of step output from agent to server.
	MsgJobLog MessageType = "job_log"
	// MsgJobResult reports the final outcome of a job.
	MsgJobResult MessageType = "job_result"
	// MsgServerClosing tells an agent the server is shutting down. The agent
	// stops sending and closes the connection itself, so every message it
	// sent before is processed; the rest waits for the next connection.
	MsgServerClosing MessageType = "server_closing"
	// MsgAck acknowledges, cumulatively, every agent message up to an ID.
	MsgAck MessageType = "ack"
	// MsgJobCancel asks an agent to stop a job; it reports it as canceled.
	MsgJobCancel MessageType = "job_cancel"
	// MsgJobStep reports that a step of a job started or ended. Servers
	// that do not know it acknowledge and drop it.
	MsgJobStep MessageType = "job_step"
)

// Message is the envelope for every WebSocket message.
//
// ID numbers the job_log, job_step, job_result and job_rejected messages of an agent
// process, increasing in the order they are sent. The agent keeps each one
// until the server acknowledges it and sends unacknowledged ones again after
// reconnecting, so the server must handle duplicates.
type Message struct {
	ID      int64           `json:"id,omitempty"`
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// JobCancel is the payload of MsgJobCancel.
type JobCancel struct {
	JobID int64 `json:"job_id"`
}

// Ack is the payload of MsgAck: the agent may forget every message with an
// ID up to and including ID.
type Ack struct {
	ID int64 `json:"id"`
}

// NewMessage encodes payload into a Message of type t.
func NewMessage(t MessageType, payload any) (Message, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("encode %s payload: %w", t, err)
	}
	return Message{Type: t, Payload: b}, nil
}

// Decode unmarshals the message payload into v.
func (m Message) Decode(v any) error {
	if err := json.Unmarshal(m.Payload, v); err != nil {
		return fmt.Errorf("decode %s payload: %w", m.Type, err)
	}
	return nil
}

// AgentHello identifies an agent to the server. RunningJobID is the job the
// agent is still working on (or has not yet reported) after a reconnect;
// the server requeues any other job it believes the agent holds.
type AgentHello struct {
	Name         string `json:"name"`
	Capacity     int    `json:"capacity"`
	RunningJobID int64  `json:"running_job_id,omitempty"`
	// Labels say what the agent offers, e.g. arm64 or gpu; it runs only
	// jobs whose labels it all has.
	Labels []string `json:"labels,omitempty"`
}

// labelRe is what a label looks like: arm64, gpu, region=eu.
var labelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._=-]{0,62}$`)

// ValidLabel reports whether s can be a label.
func ValidLabel(s string) bool { return labelRe.MatchString(s) }

// HasLabels reports whether have holds every label of need.
func HasLabels(have, need []string) bool {
	for _, n := range need {
		if !slices.Contains(have, n) {
			return false
		}
	}
	return true
}

// JobAssignment tells an agent which steps to run for a job, and which
// services to start for them first.
type JobAssignment struct {
	JobID    int64         `json:"job_id"`
	Steps    []StepSpec    `json:"steps"`
	Services []ServiceSpec `json:"services,omitempty"`
	// TraceParent is the W3C traceparent the agent's spans continue;
	// empty when the server does not trace.
	TraceParent string `json:"trace_parent,omitempty"`
	// Cache lists the directories kept between the repository's jobs.
	Cache *CacheSpec `json:"cache,omitempty"`
}

// CacheSpec names a repository's caches: one Docker volume per path on each
// agent's Docker host, mounted into every step.
type CacheSpec struct {
	// Key identifies the repository in volume names.
	Key   string   `json:"key"`
	Paths []string `json:"paths"`
	// ReadOnly mounts the caches without write access, for untrusted jobs.
	ReadOnly bool `json:"read_only,omitempty"`
}

// ServiceSpec is a container running alongside the steps of a job; steps
// reach it at host Name.
type ServiceSpec struct {
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Env     map[string]string `json:"env,omitempty"`
	Command []string          `json:"command,omitempty"`
}

// StepSpec is one step of a job: shell commands run in an image.
type StepSpec struct {
	Name           string            `json:"name"`
	Image          string            `json:"image"`
	Commands       []string          `json:"commands"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	// Secrets are extra environment variables whose values the agent masks
	// in the step's output.
	Secrets map[string]string `json:"secrets,omitempty"`
	// DependsOn lists the earlier steps (by index) that must pass before
	// this one starts; steps whose dependencies passed run at the same
	// time. Nil means the step before it, so steps run one after another.
	DependsOn []int `json:"depends_on,omitempty"`
	// Artifacts marks the step that collects the job's artifacts: the agent
	// runs it with CISID_ARTIFACT_UPLOAD=1, reads a tar stream from its
	// stdout and uploads every file in it.
	Artifacts bool `json:"artifacts,omitempty"`
}

// Needs returns the steps step i of steps waits for: its DependsOn, or the
// step before it.
func Needs(steps []StepSpec, i int) []int {
	if steps[i].DependsOn != nil {
		return steps[i].DependsOn
	}
	if i == 0 {
		return nil
	}
	return []int{i - 1}
}

// StepState is how far a step of a job got.
type StepState string

const (
	StepPending  StepState = "pending" // not started yet
	StepRunning  StepState = "running"
	StepSuccess  StepState = "success"
	StepFailed   StepState = "failed"
	StepCanceled StepState = "canceled" // stopped by a cancel, a shutdown or another step failing
	StepSkipped  StepState = "skipped"  // never started: a step it needed did not pass
)

// Finished reports whether s is a final state.
func (s StepState) Finished() bool {
	return s == StepSuccess || s == StepFailed || s == StepCanceled || s == StepSkipped
}

// StepResult is how one step of a job went. Times are the agent's.
type StepResult struct {
	State      StepState  `json:"state"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// JobStep is the payload of MsgJobStep: step Step of job JobID started
// (State running) or ended.
type JobStep struct {
	JobID int64 `json:"job_id"`
	Step  int   `json:"step"`
	StepResult
}

// JobRejected is the agent's answer to an assignment it cannot run.
type JobRejected struct {
	JobID  int64  `json:"job_id"`
	Reason string `json:"reason"`
}

// LogStream names the output stream of a JobLog chunk.
type LogStream string

const (
	StreamStdout LogStream = "stdout"
	StreamStderr LogStream = "stderr"
)

// JobLog is a chunk of output from one step. Seq increases by one per chunk
// within a job so the server can order and detect gaps.
type JobLog struct {
	JobID  int64     `json:"job_id"`
	Step   int       `json:"step"`
	Stream LogStream `json:"stream"`
	Seq    int64     `json:"seq"`
	Data   string    `json:"data"`
}

// JobResult is the final outcome of a job. FailedStep is the index of the
// step that failed or was interrupted; it is nil on success.
type JobResult struct {
	JobID      int64     `json:"job_id"`
	Status     JobStatus `json:"status"`
	ExitCode   int       `json:"exit_code"`
	FailedStep *int      `json:"failed_step,omitempty"`
	Error      string    `json:"error,omitempty"`
	// Steps says how each step went, by index; empty from agents that do
	// not report steps.
	Steps []StepResult `json:"steps,omitempty"`
}

// ReservedEnv reports whether name is an environment variable cisid sets
// for steps itself, which a pipeline or secret cannot use.
func ReservedEnv(name string) bool {
	switch name {
	case "CI", "CISID", "CISID_JOB_ID", "CISID_STEP", "CISID_REPO", "CISID_BRANCH", "CISID_COMMIT_SHA", "CISID_CLONE_URL",
		"CISID_EVENT", "CISID_PULL_REQUEST", "CISID_TARGET_BRANCH":
		return true
	}
	return false
}
