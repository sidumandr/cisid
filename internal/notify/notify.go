// Package notify announces build results: in a Slack-compatible chat, to
// a webhook, or by email.
//
// A repository's notifications say where results go and which ones: every
// failure, a branch breaking or being fixed, every pass. Where they go is
// a credential (a chat's webhook URL), so it is stored sealed with the
// secrets key. Each finished job is claimed by one server, which announces
// it once; a send that fails is retried a few times and then recorded on
// the notification.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/metrics"
	"github.com/sidumandr/cisid/internal/pipeline"
	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/pkg/models"
)

var (
	// ErrInvalid is returned for a notification that cannot be stored.
	ErrInvalid = errors.New("invalid notification")
	// ErrEmailOff is returned for an email notification on a server
	// without an SMTP relay.
	ErrEmailOff = errors.New("email is not set up on this server: set CISID_SMTP_HOST")
	// ErrNotFound is returned when a repository has no notification of
	// the given name.
	ErrNotFound = errors.New("notification not found")
	// ErrSend is returned when a notification could not be sent.
	ErrSend = errors.New("send notification")
)

const (
	defaultInterval = 5 * time.Second
	roundTimeout    = time.Minute
	sendTimeout     = 10 * time.Second
	batchSize       = 50
	// staleAfter keeps a server that was down for a long time from
	// announcing old results when it comes back.
	staleAfter = 24 * time.Hour

	maxURL        = 2048
	maxSecret     = 256
	maxRecipients = 10
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// results in the order of how much they say, least first.
var results = []string{models.ResultFailed, models.ResultBroken, models.ResultFixed, models.ResultPassed}

// DefaultOn is what a notification is sent for when it does not say.
var DefaultOn = []string{models.ResultFailed, models.ResultFixed}

// Target is where a notification goes.
type Target struct {
	URL string `json:"url,omitempty"` // slack, webhook
	// Secret signs webhook bodies (X-Cisid-Signature); optional.
	Secret string   `json:"secret,omitempty"`
	To     []string `json:"to,omitempty"` // email addresses
}

// Store keeps notifications and hands out finished jobs. *db.Store
// satisfies it.
type Store interface {
	PutNotification(ctx context.Context, n models.Notification, sealed []byte) error
	DeleteNotification(ctx context.Context, repo, name string) (bool, error)
	Notifications(ctx context.Context, repo string) ([]models.Notification, error)
	SealedNotifications(ctx context.Context, repo string) ([]db.SealedNotification, error)
	SealedNotification(ctx context.Context, repo, name string) (db.SealedNotification, error)
	RecordNotification(ctx context.Context, id int64, problem string) error
	ClaimFinishedJobs(ctx context.Context, limit int) ([]models.Job, error)
	PreviousResult(ctx context.Context, job models.Job) (models.JobStatus, bool, error)
}

var _ Store = (*db.Store)(nil)

// Option configures a Service.
type Option func(*Service)

// WithLogger sets the service's logger.
func WithLogger(log *slog.Logger) Option {
	return func(s *Service) { s.log = log }
}

// WithPublicURL links messages to the job's page under u.
func WithPublicURL(u string) Option {
	return func(s *Service) { s.publicURL = strings.TrimSuffix(u, "/") }
}

// WithMailer lets notifications go by email.
func WithMailer(m *Mailer) Option {
	return func(s *Service) { s.mailer = m }
}

// WithHTTPClient replaces the client that posts to chats and webhooks,
// which refuses link-local addresses such as cloud metadata services.
func WithHTTPClient(c *http.Client) Option {
	return func(s *Service) { s.http = c }
}

// WithInterval sets how often the service looks for finished jobs without
// being woken (default 5s).
func WithInterval(d time.Duration) Option {
	return func(s *Service) { s.interval = d }
}

// WithRetryDelays sets the waits between attempts to send; one attempt
// more than delays is made.
func WithRetryDelays(d ...time.Duration) Option {
	return func(s *Service) { s.retryDelays = d }
}

// Service keeps notifications and sends them.
type Service struct {
	store       Store
	cipher      *secrets.Cipher
	http        *http.Client
	mailer      *Mailer
	publicURL   string
	log         *slog.Logger
	interval    time.Duration
	retryDelays []time.Duration
	now         func() time.Time
	wake        chan struct{}
}

// New returns a Service that seals targets with c.
func New(store Store, c *secrets.Cipher, opts ...Option) *Service {
	s := &Service{
		store:       store,
		cipher:      c,
		http:        SafeHTTPClient(),
		log:         slog.Default(),
		interval:    defaultInterval,
		retryDelays: []time.Duration{2 * time.Second, 10 * time.Second},
		now:         time.Now,
		wake:        make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Put checks n and t and stores n, with t sealed. n.On defaults to
// DefaultOn.
func (s *Service) Put(ctx context.Context, n models.Notification, t Target) error {
	if !nameRe.MatchString(n.Name) {
		return fmt.Errorf("%w: the name must be 1 to 63 lowercase letters, digits, '.', '_' or '-'", ErrInvalid)
	}
	hint, t, err := s.checkTarget(n.Kind, t)
	if err != nil {
		return err
	}
	n.Target = hint
	if n.On, err = checkOn(n.On); err != nil {
		return err
	}
	if err := secrets.ValidateBranches(n.Branches); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	plain, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("encode target: %w", err)
	}
	sealed, err := s.cipher.Seal(n.Repo, sealName(n.Name), string(plain))
	if err != nil {
		return fmt.Errorf("seal notification %s: %w", n.Name, err)
	}
	return s.store.PutNotification(ctx, n, sealed)
}

// checkTarget checks t for kind and returns what may be shown of it.
func (s *Service) checkTarget(kind string, t Target) (string, Target, error) {
	switch kind {
	case models.NotifySlack, models.NotifyWebhook:
		if len(t.To) > 0 {
			return "", t, fmt.Errorf("%w: a %s notification has a url, not addresses", ErrInvalid, kind)
		}
		u, err := url.Parse(t.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(t.URL) > maxURL {
			return "", t, fmt.Errorf("%w: url must be an http(s) URL", ErrInvalid)
		}
		if kind == models.NotifySlack && t.Secret != "" {
			return "", t, fmt.Errorf("%w: only webhooks are signed", ErrInvalid)
		}
		if len(t.Secret) > maxSecret {
			return "", t, fmt.Errorf("%w: the secret is longer than %d bytes", ErrInvalid, maxSecret)
		}
		return u.Host, t, nil
	case models.NotifyEmail:
		if s.mailer == nil {
			return "", t, fmt.Errorf("%w: %w", ErrInvalid, ErrEmailOff)
		}
		if t.URL != "" || t.Secret != "" {
			return "", t, fmt.Errorf("%w: an email notification has addresses (to), not a url", ErrInvalid)
		}
		if len(t.To) == 0 || len(t.To) > maxRecipients {
			return "", t, fmt.Errorf("%w: give 1 to %d addresses", ErrInvalid, maxRecipients)
		}
		to := make([]string, 0, len(t.To))
		for _, a := range t.To {
			addr, err := mail.ParseAddress(strings.TrimSpace(a))
			if err != nil {
				return "", t, fmt.Errorf("%w: %q is not an email address", ErrInvalid, a)
			}
			if !slices.Contains(to, addr.Address) {
				to = append(to, addr.Address)
			}
		}
		t.To = to
		return strings.Join(to, ", "), t, nil
	}
	return "", t, fmt.Errorf("%w: kind must be slack, webhook or email", ErrInvalid)
}

// checkOn returns the results in their usual order, or DefaultOn for none.
func checkOn(on []string) ([]string, error) {
	if len(on) == 0 {
		return slices.Clone(DefaultOn), nil
	}
	for _, r := range on {
		if !slices.Contains(results, r) {
			return nil, fmt.Errorf("%w: %q is not a result; use failed, broken, fixed or passed", ErrInvalid, r)
		}
	}
	var out []string
	for _, r := range results {
		if slices.Contains(on, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// sealName binds a sealed target to its notification, apart from the
// repository's secrets.
func sealName(name string) string { return "notification " + name }

// Delete removes notification name of repo.
func (s *Service) Delete(ctx context.Context, repo, name string) error {
	ok, err := s.store.DeleteNotification(ctx, repo, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return nil
}

// List returns the notifications of repo, without where they go beyond
// their Target hint.
func (s *Service) List(ctx context.Context, repo string) ([]models.Notification, error) {
	return s.store.Notifications(ctx, repo)
}

// Test sends a test message through notification name of repo now and
// records how it went. It returns an error wrapping ErrSend when the
// message could not be sent.
func (s *Service) Test(ctx context.Context, repo, name string) error {
	n, err := s.store.SealedNotification(ctx, repo, name)
	if errors.Is(err, db.ErrNotificationNotFound) {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return err
	}
	msg := testMessage(repo, s.repoURL(repo))
	if err := s.send(ctx, n, msg, nil); err != nil {
		return fmt.Errorf("%w: %w", ErrSend, err)
	}
	return nil
}

// Wake asks the service to look for finished jobs now. It never blocks.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run announces finished jobs until ctx is canceled.
func (s *Service) Run(ctx context.Context) error {
	tick := time.NewTicker(s.interval)
	defer tick.Stop()
	for {
		s.round(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-s.wake:
		case <-tick.C:
		}
	}
}

// round announces the jobs that finished since the last one. Jobs it
// claimed are announced even if ctx is canceled meanwhile, without retries,
// as no other server will take them; it claims no more after that.
func (s *Service) round(ctx context.Context) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), roundTimeout)
	defer cancel()
	notes := map[string][]db.SealedNotification{} // by repository, for this round
	for ctx.Err() == nil {
		jobs, err := s.store.ClaimFinishedJobs(rctx, batchSize)
		if err != nil {
			s.log.ErrorContext(rctx, "claim finished jobs", "error", err)
			return
		}
		for _, job := range jobs {
			s.announce(rctx, ctx, job, notes)
		}
		if len(jobs) < batchSize {
			return
		}
	}
}

// announce sends job's result through the notifications of its repository
// that ask for it. retry is the context that, once done, stops retries.
func (s *Service) announce(ctx, retry context.Context, job models.Job, notes map[string][]db.SealedNotification) {
	if job.Status == models.JobStatusCanceled {
		return
	}
	if job.FinishedAt != nil && s.now().Sub(*job.FinishedAt) > staleAfter {
		return
	}
	list, ok := notes[job.Repo]
	if !ok {
		var err error
		if list, err = s.store.SealedNotifications(ctx, job.Repo); err != nil {
			s.log.ErrorContext(ctx, "read notifications", "repo", job.Repo, "error", err)
			return
		}
		notes[job.Repo] = list
	}

	var due []db.SealedNotification
	needPrevious := false
	for _, n := range list {
		if applies(n.Notification, job) {
			due = append(due, n)
			needPrevious = needPrevious || slices.Contains(n.On, models.ResultBroken) || slices.Contains(n.On, models.ResultFixed)
		}
	}
	if len(due) == 0 {
		return
	}

	happened := []string{models.ResultPassed}
	if job.Status == models.JobStatusFailed {
		happened = []string{models.ResultFailed}
	}
	if needPrevious {
		prev, ok, err := s.store.PreviousResult(ctx, job)
		if err != nil {
			s.log.ErrorContext(ctx, "find the build before", "job_id", job.ID, "error", err)
			return
		}
		switch {
		case job.Status == models.JobStatusFailed && (!ok || prev == models.JobStatusSuccess):
			happened = append(happened, models.ResultBroken)
		case job.Status == models.JobStatusSuccess && ok && prev == models.JobStatusFailed:
			happened = append(happened, models.ResultFixed)
		}
	}

	msg := jobMessage(job, happened[len(happened)-1], needPrevious, s.jobURL(job.ID))
	for _, n := range due {
		if slices.ContainsFunc(n.On, func(r string) bool { return slices.Contains(happened, r) }) {
			if err := s.send(ctx, n, msg, retry); err != nil {
				s.log.WarnContext(ctx, "could not send notification", "job_id", job.ID, "repo", job.Repo,
					"notification", n.Name, "error", err)
			}
		}
	}
}

// applies reports whether n is about job's branch: pushes, manual and
// scheduled builds by their branch, pull requests, if n wants them, by the
// branch they merge into.
func applies(n models.Notification, job models.Job) bool {
	branch := job.Branch
	if job.Event == models.EventPullRequest {
		if !n.PullRequests {
			return false
		}
		branch = job.TargetBranch
	}
	return len(n.Branches) == 0 || pipeline.MatchBranch(n.Branches, branch)
}

// send delivers msg through n, retrying failures that may pass until retry
// is done (nil: never), and records the outcome on n.
func (s *Service) send(ctx context.Context, n db.SealedNotification, msg Message, retry context.Context) error {
	err := s.deliver(ctx, n, msg)
	for _, d := range s.retryDelays {
		if err == nil || !isTemporary(err) || retry == nil || retry.Err() != nil {
			break
		}
		select {
		case <-time.After(d):
		case <-retry.Done():
		case <-ctx.Done():
		}
		if retry.Err() != nil || ctx.Err() != nil {
			break
		}
		err = s.deliver(ctx, n, msg)
	}

	outcome, problem := "sent", ""
	if err != nil {
		outcome, problem = "failed", err.Error()
	}
	metrics.Notifications.WithLabelValues(n.Kind, outcome).Inc()
	if rerr := s.store.RecordNotification(ctx, n.ID, problem); rerr != nil {
		s.log.ErrorContext(ctx, "record notification", "notification", n.Name, "error", rerr)
	}
	return err
}

// deliver makes one attempt to send msg through n.
func (s *Service) deliver(ctx context.Context, n db.SealedNotification, msg Message) error {
	plain, err := s.cipher.Open(n.Repo, sealName(n.Name), n.Sealed)
	if err != nil {
		return errors.New("cannot decrypt the target: was CISID_SECRET_KEY changed? Save the notification again")
	}
	var t Target
	if err := json.Unmarshal([]byte(plain), &t); err != nil {
		return fmt.Errorf("decode target: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	switch n.Kind {
	case models.NotifySlack:
		return s.postSlack(ctx, t, msg)
	case models.NotifyWebhook:
		return s.postWebhook(ctx, t, msg)
	case models.NotifyEmail:
		if s.mailer == nil {
			return ErrEmailOff
		}
		return s.mailer.Send(ctx, t.To, msg.Subject(), msg.Plain())
	}
	return fmt.Errorf("unknown kind %q", n.Kind)
}

func (s *Service) jobURL(id int64) string {
	if s.publicURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/jobs/%d", s.publicURL, id)
}

func (s *Service) repoURL(repo string) string {
	if s.publicURL == "" {
		return ""
	}
	i := strings.LastIndex(repo, "/")
	if i < 0 {
		return ""
	}
	return s.publicURL + "/repos/" + url.PathEscape(repo[:i]) + "/" + url.PathEscape(repo[i+1:])
}
