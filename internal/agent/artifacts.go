package agent

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/sidumandr/cisid/pkg/models"
)

// maxArtifactFiles bounds the files one job uploads; the server has its
// own limit.
const maxArtifactFiles = 100

// artifactsBase returns where artifacts go: the server's agent endpoint
// over HTTP, with /ws replaced by the job's path. ws://ci/agent/ws gives
// http://ci/agent.
func artifactsBase(serverURL string) (*url.URL, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}
	b := *u
	b.Scheme = "http"
	if u.Scheme == "wss" {
		b.Scheme = "https"
	}
	b.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/ws")
	b.RawPath, b.RawQuery, b.Fragment = "", "", ""
	return &b, nil
}

// syncWriter lets the step and the uploader write the step's log at once.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// collectArtifacts runs the artifacts step, whose stdout is a tar stream
// of the files to keep, and uploads each file in it as it arrives.
func (a *Agent) collectArtifacts(ctx context.Context, job models.JobAssignment, i int, env *stepEnv, stderr io.Writer) (int, error) {
	spec := job.Steps[i]
	step := toRunnerStep(job.JobID, spec, env.workspace, env.network, env.caches)
	step.Env["CISID_ARTIFACT_UPLOAD"] = "1"
	log := &syncWriter{w: stderr}

	type ran struct {
		code int
		err  error
	}
	pr, pw := io.Pipe()
	done := make(chan ran, 1)
	go func() {
		code, err := a.runStep(ctx, i, spec, step, pw, log)
		_ = pw.CloseWithError(err) // nil closes it cleanly
		done <- ran{code, err}
	}()

	upErr := a.uploadTar(ctx, job.JobID, pr, log)
	if upErr != nil {
		// The step's writes now fail, which stops it.
		_ = pr.CloseWithError(upErr)
	}
	_, _ = io.Copy(io.Discard, pr) // the tar's padding, or what is left
	r := <-done
	if upErr != nil {
		_, _ = fmt.Fprintf(log, "uploading artifacts failed: %v\n", upErr)
		return 1, fmt.Errorf("upload artifacts: %w", upErr)
	}
	return r.code, r.err
}

// uploadTar uploads every regular file of the tar stream r.
func (a *Agent) uploadTar(ctx context.Context, jobID int64, r io.Reader, log io.Writer) error {
	tr := tar.NewReader(r)
	files := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the files: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if !models.ValidArtifactName(name) {
			_, _ = fmt.Fprintf(log, "skipping %q: not a path inside the workspace\n", hdr.Name)
			continue
		}
		if files++; files > maxArtifactFiles {
			return fmt.Errorf("more than %d files", maxArtifactFiles)
		}
		if err := a.uploadFile(ctx, jobID, name, tr, hdr.Size); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(log, "uploaded %s (%s)\n", name, byteSize(hdr.Size))
	}
}

// uploadFile sends one file to the server.
func (a *Agent) uploadFile(ctx context.Context, jobID int64, name string, body io.Reader, size int64) error {
	segments := append([]string{"jobs", strconv.FormatInt(jobID, 10), "artifacts"}, strings.Split(name, "/")...)
	target := a.artifacts.JoinPath(segments...)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), io.LimitReader(body, size))
	if err != nil {
		return fmt.Errorf("upload %s: %w", name, err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(agentHeader, a.cfg.Name)
	if a.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("upload %s: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upload %s: server answered %s: %s", name, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// byteSize writes n bytes for people: 512 B, 1.5 KiB, 20.0 MiB.
func byteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
