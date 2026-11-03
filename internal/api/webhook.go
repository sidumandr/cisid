package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/metrics"
	"github.com/sidumandr/cisid/internal/telemetry"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	// maxPayloadBytes is GitHub's upper limit for webhook payloads.
	maxPayloadBytes = 25 << 20
	signaturePrefix = "sha256="
)

var (
	// ErrEmptySecret is returned when a webhook handler is created without a secret.
	ErrEmptySecret = errors.New("webhook secret must not be empty")

	errInvalidPush = errors.New("invalid push payload")
)

// maxPayloadAge rejects pushes older than this, so a captured signed
// payload cannot be replayed once its dedupe record is pruned.
const maxPayloadAge = 7 * 24 * time.Hour

// JobCreator queues the job of a webhook delivery, once per signed body.
// *db.Store satisfies it.
type JobCreator interface {
	QueueJob(ctx context.Context, job models.Job, payloadSHA256, deliveryID string) (int64, error)
}

// WebhookHandler handles GitHub webhook deliveries and queues a job for
// every push to a branch.
type WebhookHandler struct {
	secret []byte
	jobs   JobCreator
	log    *slog.Logger
	now    func() time.Time
}

// NewWebhookHandler returns a handler that verifies deliveries with secret and
// stores jobs in jobs. A nil log falls back to slog.Default().
func NewWebhookHandler(secret []byte, jobs JobCreator, log *slog.Logger) (*WebhookHandler, error) {
	if len(secret) == 0 {
		return nil, ErrEmptySecret
	}
	if log == nil {
		log = slog.Default()
	}
	return &WebhookHandler{secret: secret, jobs: jobs, log: log, now: time.Now}, nil
}

// webhookResponse is the JSON body returned for every delivery.
type webhookResponse struct {
	Status string `json:"status"`
	JobID  int64  `json:"job_id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// pushPayload holds the fields of a GitHub push event that cisid uses.
type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
		// PushedAt is a Unix time in push events (a string elsewhere).
		PushedAt json.RawMessage `json:"pushed_at"`
	} `json:"repository"`
}

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPayloadBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}

	delivery := r.Header.Get("X-GitHub-Delivery")
	if !validSignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		h.log.WarnContext(r.Context(), "rejected webhook with invalid signature",
			"delivery", delivery, "remote_addr", r.RemoteAddr)
		metrics.Webhooks.WithLabelValues("invalid_signature").Inc()
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "ping":
		writeJSON(w, http.StatusOK, webhookResponse{Status: "pong"})
	case "push":
		h.handlePush(w, r, body, delivery)
	default:
		writeJSON(w, http.StatusOK, webhookResponse{
			Status: "ignored",
			Reason: fmt.Sprintf("unsupported event %q", event),
		})
	}
}

func (h *WebhookHandler) handlePush(w http.ResponseWriter, r *http.Request, body []byte, delivery string) {
	ctx := r.Context()

	job, reason, err := jobFromPush(body, h.now())
	if err != nil {
		metrics.Webhooks.WithLabelValues("invalid_payload").Inc()
		h.log.WarnContext(ctx, "rejected push payload", "delivery", delivery, "error", err)
		writeError(w, http.StatusBadRequest, "invalid push payload")
		return
	}
	if reason != "" {
		metrics.Webhooks.WithLabelValues("ignored").Inc()
		writeJSON(w, http.StatusOK, webhookResponse{Status: "ignored", Reason: reason})
		return
	}

	// The job continues this request's trace when it is dispatched.
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(telemetry.AttrDeliveryID.String(delivery), telemetry.AttrRepo.String(job.Repo),
		telemetry.AttrBranch.String(job.Branch), telemetry.AttrCommit.String(job.CommitSHA))
	job.TraceParent = telemetry.TraceParent(ctx)

	sum := sha256.Sum256(body)
	id, err := h.jobs.QueueJob(ctx, job, hex.EncodeToString(sum[:]), delivery)
	if errors.Is(err, db.ErrDuplicateDelivery) {
		metrics.Webhooks.WithLabelValues("duplicate").Inc()
		h.log.WarnContext(ctx, "ignoring a payload that already queued a job", "delivery", delivery)
		writeJSON(w, http.StatusOK, webhookResponse{Status: "ignored", Reason: "duplicate delivery"})
		return
	}
	if err != nil {
		metrics.Webhooks.WithLabelValues("error").Inc()
		h.log.ErrorContext(ctx, "queue job", "delivery", delivery, "repo", job.Repo, "error", err)
		writeError(w, http.StatusInternalServerError, "could not queue job")
		return
	}

	metrics.Webhooks.WithLabelValues("queued").Inc()
	span.SetAttributes(telemetry.AttrJobID.Int64(id))
	h.log.InfoContext(ctx, "job queued", "delivery", delivery, "job_id", id,
		"repo", job.Repo, "branch", job.Branch, "commit", job.CommitSHA)
	writeJSON(w, http.StatusAccepted, webhookResponse{Status: "queued", JobID: id})
}

// jobFromPush builds a pending job from a push payload. A non-empty reason
// means the push is valid but does not trigger a job.
func jobFromPush(body []byte, now time.Time) (models.Job, string, error) {
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return models.Job{}, "", fmt.Errorf("decode push payload: %w", err)
	}

	branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
	if !ok || branch == "" {
		return models.Job{}, "not a branch push", nil
	}
	if p.Deleted {
		return models.Job{}, "branch deleted", nil
	}
	if pushed, err := strconv.ParseInt(string(p.Repository.PushedAt), 10, 64); err == nil && pushed > 0 &&
		now.Sub(time.Unix(pushed, 0)) > maxPayloadAge {
		return models.Job{}, "stale delivery", nil
	}

	switch {
	case p.Repository.FullName == "":
		return models.Job{}, "", fmt.Errorf("%w: repository.full_name is empty", errInvalidPush)
	case p.Repository.CloneURL == "":
		return models.Job{}, "", fmt.Errorf("%w: repository.clone_url is empty", errInvalidPush)
	case !validCommitSHA(p.After):
		return models.Job{}, "", fmt.Errorf("%w: commit SHA %q", errInvalidPush, p.After)
	}

	return models.Job{
		Repo:      p.Repository.FullName,
		CloneURL:  p.Repository.CloneURL,
		Branch:    branch,
		CommitSHA: p.After,
		Status:    models.JobStatusPending,
	}, "", nil
}

// validSignature reports whether header is the HMAC-SHA256 of body under
// secret, in GitHub's "sha256=<hex>" format.
func validSignature(secret, body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// validCommitSHA accepts SHA-1 (40) and SHA-256 (64) hex object IDs.
func validCommitSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, webhookResponse{Status: "error", Reason: reason})
}
