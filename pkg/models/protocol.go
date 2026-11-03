package models

import (
	"encoding/json"
	"fmt"
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
)

// Message is the envelope for every WebSocket message.
type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
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
}

// JobAssignment tells an agent which steps to run for a job.
type JobAssignment struct {
	JobID int64      `json:"job_id"`
	Steps []StepSpec `json:"steps"`
}

// StepSpec is one step of a job: shell commands run in an image.
type StepSpec struct {
	Name           string            `json:"name"`
	Image          string            `json:"image"`
	Commands       []string          `json:"commands"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
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
}
