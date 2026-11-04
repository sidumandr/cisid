package agent

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/pkg/models"
)

func TestArtifactsBase(t *testing.T) {
	for in, want := range map[string]string{
		"ws://ci.example.com/agent/ws":      "http://ci.example.com/agent",
		"wss://ci.example.com/ci/agent/ws/": "https://ci.example.com/ci/agent",
		"ws://127.0.0.1:8080/agent/ws":      "http://127.0.0.1:8080/agent",
	} {
		got, err := artifactsBase(in)
		require.NoError(t, err)
		assert.Equal(t, want, got.String(), in)
	}
}

// tarOf builds a tar stream of the given files, in order; a name ending
// in / is a directory.
func tarOf(t *testing.T, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if strings.HasSuffix(f[0], "/") {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: f[0], Typeflag: tar.TypeDir, Mode: 0o755}))
			continue
		}
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1]))}))
		_, err := tw.Write([]byte(f[1]))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func collectJob(id int64) models.JobAssignment {
	return models.JobAssignment{JobID: id, Steps: []models.StepSpec{
		{Name: "build", Image: "golang:1.27", Commands: []string{"make"}},
		{Name: "artifacts", Image: "collect:1", Commands: []string{"tar -cf - dist"}, Artifacts: true},
	}}
}

func TestAgentUploadsArtifacts(t *testing.T) {
	fs := newFakeServer(t)
	fs.token = "agent-token-0123456789"
	r := &mockRunner{}
	stream := tarOf(t,
		[2]string{"dist/", ""},
		[2]string{"dist/app linux.bin", "binary\x00content"},
		[2]string{"./report.xml", "<ok/>"},
		[2]string{"../escape.txt", "no"},
	)
	r.On("Run", mock.Anything, image("golang:1.27"), mock.Anything, mock.Anything).Return(0, nil).Once()
	r.On("Run", mock.Anything, mock.MatchedBy(func(s runner.Step) bool {
		return s.Image == "collect:1" && s.Env["CISID_ARTIFACT_UPLOAD"] == "1"
	}), mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		_, err := io.Copy(args.Get(2).(io.Writer), bytes.NewReader(stream))
		assert.NoError(t, err)
	}).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r, func(c *Config) { c.Token = fs.token })
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, collectJob(70))

	logs, res := sc.collectJob(t, 70)
	assert.Equal(t, models.JobStatusSuccess, res.Status)
	assert.Equal(t, []models.StepState{models.StepSuccess, models.StepSuccess}, sc.stepStates())

	ups := fs.uploaded()
	require.Len(t, ups, 2, "regular files inside the workspace only")
	assert.Equal(t, "/agent/jobs/70/artifacts/dist/app linux.bin", ups[0].path)
	assert.Equal(t, "binary\x00content", ups[0].body)
	assert.EqualValues(t, len("binary\x00content"), ups[0].length)
	assert.Equal(t, "test-agent", ups[0].agent)
	assert.Equal(t, "Bearer agent-token-0123456789", ups[0].auth)
	assert.Equal(t, "/agent/jobs/70/artifacts/report.xml", ups[1].path)

	var text strings.Builder
	for _, l := range logs {
		if l.Step == 1 {
			text.WriteString(l.Data)
		}
	}
	assert.Contains(t, text.String(), "uploaded dist/app linux.bin (14 B)")
	assert.Contains(t, text.String(), `skipping "../escape.txt"`)
	assert.NotContains(t, text.String(), "binary", "the tar stream is not logged")
	r.AssertExpectations(t)
}

func TestAgentFailsJobWhenUploadIsRefused(t *testing.T) {
	fs := newFakeServer(t)
	fs.uploadStatus = http.StatusRequestEntityTooLarge
	r := &mockRunner{}
	r.On("Run", mock.Anything, image("golang:1.27"), mock.Anything, mock.Anything).Return(0, nil).Once()
	r.On("Run", mock.Anything, image("collect:1"), mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		// Like a container whose output stops being read: the write fails.
		_, _ = io.Copy(args.Get(2).(io.Writer), bytes.NewReader(tarOf(t, [2]string{"big.bin", strings.Repeat("x", 1<<16)}, [2]string{"more.bin", "y"})))
	}).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, collectJob(71))

	_, res := sc.collectJob(t, 71)
	assert.Equal(t, models.JobStatusFailed, res.Status)
	assert.Equal(t, intPtr(1), res.FailedStep)
	assert.Contains(t, res.Error, "413")
	assert.Len(t, fs.uploaded(), 1, "it stops at the first refusal")
}

func TestAgentWithoutArtifactsStepUploadsNothing(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	r.On("Run", mock.Anything, mock.MatchedBy(func(s runner.Step) bool {
		_, set := s.Env["CISID_ARTIFACT_UPLOAD"]
		return !set
	}), mock.Anything, mock.Anything).Run(writeTo("plain output\n", "")).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{JobID: 72, Steps: []models.StepSpec{
		{Name: "build", Image: "alpine:3.22", Commands: []string{"echo"}},
	}})
	_, res := sc.collectJob(t, 72)
	assert.Equal(t, models.JobStatusSuccess, res.Status)
	assert.Empty(t, fs.uploaded())
}

func TestByteSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KiB", 20 << 20: "20.0 MiB", 3 << 30: "3.0 GiB"} {
		assert.Equal(t, want, byteSize(n), n)
	}
}
