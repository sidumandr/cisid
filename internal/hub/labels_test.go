package hub

import (
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/pipeline"
	"github.com/sidumandr/cisid/pkg/models"
)

func TestHubRequeuesJobForAgentWithLabels(t *testing.T) {
	requeued := make(chan struct{}, 1)
	claimedNext := make(chan struct{}, 10)
	needsARM := pipeline.Plan{Assignment: models.JobAssignment{Steps: cloneSteps}, Labels: []string{"arm64"}}

	f := newFixture(t, func(s *mockStore, p *mockPlanner) {
		s.On("ClaimJob", mock.Anything, "plain", mock.Anything).Return(job(42), nil).Once()
		s.On("ClaimJob", mock.Anything, "plain", mock.Anything).Return(models.Job{}, db.ErrNoPendingJob).
			Run(signal(claimedNext))
		p.On("Plan", mock.Anything, job(42)).Return(needsARM, nil)
		s.On("RequeueForLabels", mock.Anything, int64(42), "plain", []string{"arm64"}).Return(nil).
			Run(signal(requeued)).Once()
		// An agent that has the label gets it.
		s.On("ClaimJob", mock.Anything, "arm", mock.Anything).Return(job(42), nil).Once()
		s.On("ClaimJob", mock.Anything, "arm", mock.Anything).Return(models.Job{}, db.ErrNoPendingJob)
	})

	plain := f.connect(t, "plain", 0)
	waitFor(t, requeued, "the job to go back for an agent with arm64")
	waitFor(t, claimedNext, "the agent to be offered another job")
	plain.expectNoMessage(t)

	arm := f.dial(t)
	arm.send(t, models.MsgAgentHello, models.AgentHello{Name: "arm", Capacity: 1, Labels: []string{"arm64", "gpu"}})
	assert.Equal(t, int64(42), arm.expectAssignment(t).JobID)

	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	assert.Contains(t, f.store.claimLabels, []string{"arm64", "gpu"}, "claims say what the agent offers")
	assert.Contains(t, f.store.claimLabels, []string(nil), "an agent without labels offers none")
}

func TestHubRejectsInvalidLabels(t *testing.T) {
	f := newFixture(t, nil)
	a := f.dial(t)
	a.send(t, models.MsgAgentHello, models.AgentHello{Name: "x", Capacity: 1, Labels: []string{"has space"}})
	assert.Equal(t, websocket.StatusPolicyViolation, a.expectClosed(t))
}

func TestHasLabels(t *testing.T) {
	assert.True(t, models.HasLabels(nil, nil))
	assert.True(t, models.HasLabels([]string{"arm64", "gpu"}, []string{"gpu"}))
	assert.False(t, models.HasLabels([]string{"arm64"}, []string{"arm64", "gpu"}))
	assert.False(t, models.HasLabels(nil, []string{"gpu"}))
}
