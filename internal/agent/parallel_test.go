package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

// barrier lets n callers through only once all n have arrived, so a test
// sees steps run at the same time; it gives up after testTimeout.
type barrier struct {
	mu      sync.Mutex
	arrived int
	n       int
	all     chan struct{}
}

func newBarrier(n int) *barrier { return &barrier{n: n, all: make(chan struct{})} }

func (b *barrier) wait() bool {
	b.mu.Lock()
	b.arrived++
	if b.arrived == b.n {
		close(b.all)
	}
	b.mu.Unlock()
	select {
	case <-b.all:
		return true
	case <-time.After(testTimeout):
		return false
	}
}

func parallelSteps() []models.StepSpec {
	return []models.StepSpec{
		{Name: "clone", Image: "clone:1", Commands: []string{"git clone"}},
		{Name: "lint", Image: "lint:1", Commands: []string{"lint"}, DependsOn: []int{0}},
		{Name: "unit", Image: "unit:1", Commands: []string{"test"}, DependsOn: []int{0}},
		{Name: "report", Image: "report:1", Commands: []string{"report"}, DependsOn: []int{1, 2}},
	}
}

func TestAgentRunsIndependentStepsAtOnce(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	both := newBarrier(2)
	var (
		mu    sync.Mutex
		order []string
	)
	record := func(name string) func(mock.Arguments) {
		return func(mock.Arguments) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
		}
	}
	r.On("Run", mock.Anything, image("clone:1"), mock.Anything, mock.Anything).Run(record("clone")).Return(0, nil).Once()
	for _, img := range []string{"lint:1", "unit:1"} {
		r.On("Run", mock.Anything, image(img), mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			assert.True(t, both.wait(), "lint and unit run at the same time")
			record(img)(args)
		}).Return(0, nil).Once()
	}
	r.On("Run", mock.Anything, image("report:1"), mock.Anything, mock.Anything).Run(record("report")).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{JobID: 60, Steps: parallelSteps()})

	_, res := sc.collectJob(t, 60)
	assert.Equal(t, models.JobResult{JobID: 60, Status: models.JobStatusSuccess}, res)
	assert.Equal(t, []models.StepState{models.StepSuccess, models.StepSuccess, models.StepSuccess, models.StepSuccess}, sc.stepStates())
	require.Len(t, order, 4)
	assert.Equal(t, "clone", order[0])
	assert.ElementsMatch(t, []string{"lint:1", "unit:1"}, order[1:3])
	assert.Equal(t, "report", order[3], "report waits for both")
	r.AssertExpectations(t)
}

func TestAgentStopsOtherStepsWhenOneFails(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	unitStarted := make(chan struct{})
	r.On("Run", mock.Anything, image("clone:1"), mock.Anything, mock.Anything).Return(0, nil).Once()
	r.On("Run", mock.Anything, image("unit:1"), mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		close(unitStarted)
		<-args.Get(0).(context.Context).Done() // like the runner: stop on cancel
	}).Return(-1, context.Canceled).Once()
	r.On("Run", mock.Anything, image("lint:1"), mock.Anything, mock.Anything).Run(func(mock.Arguments) {
		<-unitStarted
	}).Return(2, nil).Once()
	r.On("RemoveVolume", mock.MatchedBy(notCanceled), mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{JobID: 61, Steps: parallelSteps()})

	_, res := sc.collectJob(t, 61)
	assert.Equal(t, models.JobResult{JobID: 61, Status: models.JobStatusFailed, ExitCode: 2, FailedStep: intPtr(1)}, res,
		"the step that failed, not the one it stopped")
	assert.Equal(t, []models.StepState{models.StepSuccess, models.StepFailed, models.StepCanceled, models.StepSkipped}, sc.stepStates())
	r.AssertExpectations(t)
	r.AssertNotCalled(t, "Run", mock.Anything, image("report:1"), mock.Anything, mock.Anything)
}

func TestAgentCancelStopsAllRunningSteps(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	both := newBarrier(2)
	r.On("Run", mock.Anything, image("clone:1"), mock.Anything, mock.Anything).Return(0, nil).Once()
	for _, img := range []string{"lint:1", "unit:1"} {
		r.On("Run", mock.Anything, image(img), mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			both.wait()
			<-args.Get(0).(context.Context).Done()
		}).Return(-1, context.Canceled).Once()
	}
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{JobID: 62, Steps: parallelSteps()})
	// Wait until both run, then cancel.
	running := 0
	for running < 2 {
		var s models.JobStep
		sc.expect(t, models.MsgJobStep, &s)
		if s.State == models.StepRunning && s.Step > 0 {
			running++
		}
	}
	sc.send(t, models.MsgJobCancel, models.JobCancel{JobID: 62})

	_, res := sc.collectJob(t, 62)
	assert.Equal(t, models.JobStatusCanceled, res.Status)
	assert.Equal(t, intPtr(1), res.FailedStep, "the first step it stopped")
	assert.Equal(t, []models.StepState{models.StepSuccess, models.StepCanceled, models.StepCanceled, models.StepSkipped}, sc.stepStates())
	r.AssertExpectations(t)
}
