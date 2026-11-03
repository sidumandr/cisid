package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageRoundTrip(t *testing.T) {
	step := 1
	want := JobResult{JobID: 42, Status: JobStatusFailed, ExitCode: 2, FailedStep: &step}

	msg, err := NewMessage(MsgJobResult, want)
	require.NoError(t, err)

	wire, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"type":"job_result","payload":{"job_id":42,"status":"failed","exit_code":2,"failed_step":1}}`,
		string(wire))

	var decoded Message
	require.NoError(t, json.Unmarshal(wire, &decoded))
	var got JobResult
	require.NoError(t, decoded.Decode(&got))
	assert.Equal(t, want, got)
}

func TestMessageDecodeError(t *testing.T) {
	msg := Message{Type: MsgJobAssigned, Payload: json.RawMessage(`{"job_id":"not-a-number"}`)}

	var a JobAssignment
	err := msg.Decode(&a)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode job_assigned payload")
}
