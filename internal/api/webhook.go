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
	"github.com/sidumandr/cisid/internal/forge"
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
	// ErrUnknownWebhookForge is returned for a forge with no webhook support.
	ErrUnknownWebhookForge = errors.New("no webhooks for this forge")

	errInvalidPayload = errors.New("invalid payload")
)

// maxPayloadAge rejects deliveries older than this, so a captured signed
// payload cannot be replayed once its dedupe record is pruned.
const maxPayloadAge = 7 * 24 * time.Hour

// forkReason is why a pull request from a fork is not built by default.
const forkReason = "pull request from a fork (CISID_BUILD_FORKS is off)"

// JobCreator queues the job of a webhook delivery, once per signed body,
// and cancels the jobs of closed pull requests. *db.Store satisfies it.
type JobCreator interface {
	QueueJob(ctx context.Context, job models.Job, payloadSHA256, deliveryID string) (int64, error)
	CancelPullRequest(ctx context.Context, repo string, number int) ([]int64, error)
}

// CommitResolver expands an abbreviated commit hash of a repository path.
// *bitbucket.Client satisfies it.
type CommitResolver interface {
	Commit(ctx context.Context, repo, rev string) (string, error)
}

// WebhookHandler handles one forge's webhook deliveries and queues a job
// for every push to a branch and every change to a pull request.
type WebhookHandler struct {
	forge        string
	secret       []byte
	jobs         JobCreator
	log          *slog.Logger
	now          func() time.Time
	buildForks   bool
	approveForks bool
	commits      CommitResolver
	src          webhookSource
}

// WebhookOption configures a WebhookHandler.
type WebhookOption func(*WebhookHandler)

// WithForkApproval holds the jobs of pull requests from forks, built with
// WithForkBuilds, until a maintainer approves each.
func WithForkApproval(enabled bool) WebhookOption {
	return func(h *WebhookHandler) { h.approveForks = enabled }
}

// WithForkBuilds builds pull requests from forks too, without secrets.
// They run code from outside the repository on the agents.
func WithForkBuilds(enabled bool) WebhookOption {
	return func(h *WebhookHandler) { h.buildForks = enabled }
}

// WithForge takes deliveries from forge (forge.Gitea, forge.Forgejo,
// forge.GitLab or forge.Bitbucket) instead of GitHub.
func WithForge(name string) WebhookOption {
	return func(h *WebhookHandler) { h.forge = name }
}

// WithCommitResolver expands abbreviated commit hashes, which Bitbucket's
// pull request events carry. Bitbucket's handler needs it.
func WithCommitResolver(c CommitResolver) WebhookOption {
	return func(h *WebhookHandler) { h.commits = c }
}

// NewWebhookHandler returns a handler that verifies deliveries with secret and
// stores jobs in jobs. A nil log falls back to slog.Default().
func NewWebhookHandler(secret []byte, jobs JobCreator, log *slog.Logger, opts ...WebhookOption) (*WebhookHandler, error) {
	if len(secret) == 0 {
		return nil, ErrEmptySecret
	}
	if log == nil {
		log = slog.Default()
	}
	h := &WebhookHandler{forge: forge.GitHub, secret: secret, jobs: jobs, log: log, now: time.Now}
	for _, opt := range opts {
		opt(h)
	}
	switch h.forge {
	case forge.GitHub:
		h.src = githubSource{}
	case forge.Gitea, forge.Forgejo:
		h.src = giteaSource{name: h.forge}
	case forge.GitLab:
		h.src = gitlabSource{}
	case forge.Bitbucket:
		if h.commits == nil {
			return nil, fmt.Errorf("%w: Bitbucket needs a commit resolver", ErrUnknownWebhookForge)
		}
		h.src = bitbucketSource{commits: h.commits}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownWebhookForge, h.forge)
	}
	return h, nil
}

// webhookSource knows one forge's deliveries.
type webhookSource interface {
	// verify reports whether a delivery comes from the forge.
	verify(secret []byte, header http.Header, body []byte) bool
	// deliveryID names the delivery in logs; it is not trusted.
	deliveryID(header http.Header) string
	// parse turns a verified delivery into what it asks for.
	parse(ctx context.Context, header http.Header, body []byte, now time.Time) (webhookAction, error)
}

// webhookAction is what a delivery asks for: an answer to a ping, jobs to
// queue, a closed pull request whose jobs to cancel, or nothing (reason).
type webhookAction struct {
	kind   string // "push" or "pull request", for errors and logs
	pong   bool
	jobs   []models.Job
	closed *closedPullRequest
	reason string
}

type closedPullRequest struct {
	repo   string
	number int
}

func ignore(reason string) (webhookAction, error) {
	return webhookAction{reason: reason}, nil
}

// webhookResponse is the JSON body returned for every delivery.
type webhookResponse struct {
	Status string `json:"status"`
	JobID  int64  `json:"job_id,omitempty"`
	// JobIDs lists every job when a delivery queued several, e.g. a
	// Bitbucket push to more than one branch.
	JobIDs []int64 `json:"job_ids,omitempty"`
	Reason string  `json:"reason,omitempty"`
	// Canceled lists the jobs a closed pull request stopped.
	Canceled []int64 `json:"canceled,omitempty"`
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

	ctx := r.Context()
	delivery := h.src.deliveryID(r.Header)
	if !h.src.verify(h.secret, r.Header, body) {
		h.log.WarnContext(ctx, "rejected webhook with invalid signature",
			"forge", h.forge, "delivery", delivery, "remote_addr", r.RemoteAddr)
		metrics.Webhooks.WithLabelValues("invalid_signature").Inc()
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	act, err := h.src.parse(ctx, r.Header, body, h.now())
	switch {
	case errors.Is(err, forge.ErrTemporary):
		metrics.Webhooks.WithLabelValues("error").Inc()
		h.log.WarnContext(ctx, "could not process "+act.kind+" delivery", "forge", h.forge, "delivery", delivery, "error", err)
		writeError(w, http.StatusServiceUnavailable, h.forge+" is unavailable, redeliver later")
		return
	case err != nil:
		metrics.Webhooks.WithLabelValues("invalid_payload").Inc()
		h.log.WarnContext(ctx, "rejected "+act.kind+" payload", "forge", h.forge, "delivery", delivery, "error", err)
		writeError(w, http.StatusBadRequest, "invalid "+act.kind+" payload")
		return
	case act.pong:
		writeJSON(w, http.StatusOK, webhookResponse{Status: "pong"})
		return
	case act.closed != nil:
		h.cancel(w, r, delivery, *act.closed)
		return
	}

	var jobs []models.Job
	for _, job := range act.jobs {
		if job.Fork && !h.buildForks {
			act.reason = forkReason
			continue
		}
		job.NeedsApproval = job.Fork && h.approveForks
		jobs = append(jobs, job)
	}
	if len(jobs) == 0 {
		if act.reason == "" {
			act.reason = "nothing to build"
		}
		metrics.Webhooks.WithLabelValues("ignored").Inc()
		writeJSON(w, http.StatusOK, webhookResponse{Status: "ignored", Reason: act.reason})
		return
	}
	h.queue(w, r, body, delivery, jobs)
}

// cancel stops the jobs of a closed pull request, which needs no more builds.
func (h *WebhookHandler) cancel(w http.ResponseWriter, r *http.Request, delivery string, pr closedPullRequest) {
	ctx := r.Context()
	ids, err := h.jobs.CancelPullRequest(ctx, pr.repo, pr.number)
	if err != nil {
		metrics.Webhooks.WithLabelValues("error").Inc()
		h.log.ErrorContext(ctx, "cancel jobs of closed pull request", "delivery", delivery,
			"repo", pr.repo, "pull_request", pr.number, "error", err)
		writeError(w, http.StatusInternalServerError, "could not cancel jobs")
		return
	}
	metrics.Webhooks.WithLabelValues("canceled").Inc()
	if len(ids) > 0 {
		h.log.InfoContext(ctx, "pull request closed, jobs canceled", "delivery", delivery,
			"repo", pr.repo, "pull_request", pr.number, "jobs", ids)
	}
	writeJSON(w, http.StatusOK, webhookResponse{Status: "canceled", Reason: "pull request closed", Canceled: ids})
}

// queue queues the jobs of a delivery, each once per signed body.
func (h *WebhookHandler) queue(w http.ResponseWriter, r *http.Request, body []byte, delivery string, jobs []models.Job) {
	ctx := r.Context()

	// The jobs continue this request's trace when they are dispatched.
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(telemetry.AttrDeliveryID.String(delivery), telemetry.AttrRepo.String(jobs[0].Repo),
		telemetry.AttrBranch.String(jobs[0].Branch), telemetry.AttrCommit.String(jobs[0].CommitSHA))
	if jobs[0].PullRequest > 0 {
		span.SetAttributes(telemetry.AttrPullRequest.Int(jobs[0].PullRequest))
	}

	sum := sha256.Sum256(body)
	key := hex.EncodeToString(sum[:])
	var ids []int64
	for i, job := range jobs {
		job.TraceParent = telemetry.TraceParent(ctx)
		dedupe := key
		if i > 0 {
			dedupe = fmt.Sprintf("%s/%d", key, i)
		}
		id, err := h.jobs.QueueJob(ctx, job, dedupe, delivery)
		if errors.Is(err, db.ErrDuplicateDelivery) {
			metrics.Webhooks.WithLabelValues("duplicate").Inc()
			h.log.WarnContext(ctx, "ignoring a payload that already queued a job", "delivery", delivery)
			continue
		}
		if err != nil {
			metrics.Webhooks.WithLabelValues("error").Inc()
			h.log.ErrorContext(ctx, "queue job", "delivery", delivery, "repo", job.Repo, "error", err)
			writeError(w, http.StatusInternalServerError, "could not queue job")
			return
		}
		metrics.Webhooks.WithLabelValues("queued").Inc()
		h.log.InfoContext(ctx, "job queued", "forge", h.forge, "delivery", delivery, "job_id", id, "event", job.Event,
			"repo", job.Repo, "branch", job.Branch, "commit", job.CommitSHA, "pull_request", job.PullRequest)
		ids = append(ids, id)
	}

	switch len(ids) {
	case 0:
		writeJSON(w, http.StatusOK, webhookResponse{Status: "ignored", Reason: "duplicate delivery"})
	case 1:
		span.SetAttributes(telemetry.AttrJobID.Int64(ids[0]))
		writeJSON(w, http.StatusAccepted, webhookResponse{Status: "queued", JobID: ids[0]})
	default:
		span.SetAttributes(telemetry.AttrJobID.Int64(ids[0]))
		writeJSON(w, http.StatusAccepted, webhookResponse{Status: "queued", JobID: ids[0], JobIDs: ids})
	}
}

// stale reports whether a delivery made at t is too old to trust.
func stale(t, now time.Time) bool {
	return !t.IsZero() && now.Sub(t) > maxPayloadAge
}

// unixTime reads a Unix time; anything else is the zero time.
func unixTime(raw json.RawMessage) time.Time {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// validHMAC reports whether hexSig is the HMAC-SHA256 of body under secret.
func validHMAC(secret, body []byte, hexSig string) bool {
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// validSignature reports whether header is the HMAC-SHA256 of body under
// secret, in GitHub's "sha256=<hex>" format.
func validSignature(secret, body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(header, signaturePrefix)
	return ok && validHMAC(secret, body, hexSig)
}

// validCommitSHA accepts SHA-1 (40) and SHA-256 (64) hex object IDs.
func validCommitSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil
}

// zeroSHA reports whether sha is all zeros, which forges send for a
// deleted branch.
func zeroSHA(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, webhookResponse{Status: "error", Reason: reason})
}
