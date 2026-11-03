package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/metrics"
	"github.com/sidumandr/cisid/internal/telemetry"
	"github.com/sidumandr/cisid/pkg/models"
)

const testSecret = "s3cr3t-webhook-key"

type mockJobCreator struct {
	mock.Mock
}

func (m *mockJobCreator) QueueJob(ctx context.Context, job models.Job, payloadSHA256, deliveryID string) (int64, error) {
	args := m.Called(ctx, job, payloadSHA256, deliveryID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockJobCreator) CancelPullRequest(ctx context.Context, repo string, number int) ([]int64, error) {
	args := m.Called(ctx, repo, number)
	ids, _ := args.Get(0).([]int64)
	return ids, args.Error(1)
}

func loadPayload(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

func newTestHandler(t *testing.T, store JobCreator) *WebhookHandler {
	t.Helper()
	h, err := NewWebhookHandler([]byte(testSecret), store, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return h
}

func TestNewWebhookHandlerRejectsEmptySecret(t *testing.T) {
	_, err := NewWebhookHandler(nil, &mockJobCreator{}, nil)
	assert.ErrorIs(t, err, ErrEmptySecret)
}

func TestWebhookHandler(t *testing.T) {
	pushBody := loadPayload(t, "push.json")
	errStore := errors.New("connection refused")

	tests := []struct {
		name       string
		method     string
		event      string
		body       []byte
		signature  string // empty means: sign body with testSecret
		setup      func(m *mockJobCreator)
		wantStatus int
		wantBody   webhookResponse
	}{
		{
			name:  "push to main is queued",
			event: "push",
			body:  pushBody,
			setup: func(m *mockJobCreator) {
				m.On("QueueJob", mock.Anything, models.Job{
					Repo:      "sidumandr/cisid",
					CloneURL:  "https://github.com/sidumandr/cisid.git",
					Branch:    "main",
					CommitSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246",
					Status:    models.JobStatusPending,
					Event:     models.EventPush,
				}, mock.Anything, mock.Anything).
					Return(int64(42), nil).Once()
			},
			wantStatus: http.StatusAccepted,
			wantBody:   webhookResponse{Status: "queued", JobID: 42},
		},
		{
			name:  "push to nested branch keeps full branch name",
			event: "push",
			body:  loadPayload(t, "push_nested_branch.json"),
			setup: func(m *mockJobCreator) {
				m.On("QueueJob", mock.Anything, models.Job{
					Repo:      "sidumandr/cisid",
					CloneURL:  "https://github.com/sidumandr/cisid.git",
					Branch:    "feature/webhook",
					CommitSHA: "a3f1c9e2b7d4085f6e1a2b3c4d5e6f708192a3b4",
					Status:    models.JobStatusPending,
					Event:     models.EventPush,
				}, mock.Anything, mock.Anything).
					Return(int64(7), nil).Once()
			},
			wantStatus: http.StatusAccepted,
			wantBody:   webhookResponse{Status: "queued", JobID: 7},
		},
		{
			name:       "branch deletion is ignored",
			event:      "push",
			body:       loadPayload(t, "push_branch_deleted.json"),
			wantStatus: http.StatusOK,
			wantBody:   webhookResponse{Status: "ignored", Reason: "branch deleted"},
		},
		{
			name:       "tag push is ignored",
			event:      "push",
			body:       loadPayload(t, "push_tag.json"),
			wantStatus: http.StatusOK,
			wantBody:   webhookResponse{Status: "ignored", Reason: "not a branch push"},
		},
		{
			name:       "ping answers pong",
			event:      "ping",
			body:       loadPayload(t, "ping.json"),
			wantStatus: http.StatusOK,
			wantBody:   webhookResponse{Status: "pong"},
		},
		{
			name:       "unsupported event is ignored",
			event:      "issues",
			body:       pushBody,
			wantStatus: http.StatusOK,
			wantBody:   webhookResponse{Status: "ignored", Reason: `unsupported event "issues"`},
		},
		{
			name:       "missing signature is rejected",
			event:      "push",
			body:       pushBody,
			signature:  "-",
			wantStatus: http.StatusUnauthorized,
			wantBody:   webhookResponse{Status: "error", Reason: "invalid signature"},
		},
		{
			name:       "signature with wrong secret is rejected",
			event:      "push",
			body:       pushBody,
			signature:  sign("wrong-secret", pushBody),
			wantStatus: http.StatusUnauthorized,
			wantBody:   webhookResponse{Status: "error", Reason: "invalid signature"},
		},
		{
			name:       "signature without sha256 prefix is rejected",
			event:      "push",
			body:       pushBody,
			signature:  sign(testSecret, pushBody)[len(signaturePrefix):],
			wantStatus: http.StatusUnauthorized,
			wantBody:   webhookResponse{Status: "error", Reason: "invalid signature"},
		},
		{
			name:       "non-hex signature is rejected",
			event:      "push",
			body:       pushBody,
			signature:  signaturePrefix + "not-hex",
			wantStatus: http.StatusUnauthorized,
			wantBody:   webhookResponse{Status: "error", Reason: "invalid signature"},
		},
		{
			name:       "tampered body is rejected",
			event:      "push",
			body:       bytes.Replace(pushBody, []byte("refs/heads/main"), []byte("refs/heads/evil"), 1),
			signature:  sign(testSecret, pushBody),
			wantStatus: http.StatusUnauthorized,
			wantBody:   webhookResponse{Status: "error", Reason: "invalid signature"},
		},
		{
			name:       "invalid JSON is rejected",
			event:      "push",
			body:       []byte(`{"ref": "refs/heads/main",`),
			wantStatus: http.StatusBadRequest,
			wantBody:   webhookResponse{Status: "error", Reason: "invalid push payload"},
		},
		{
			name:       "missing commit SHA is rejected",
			event:      "push",
			body:       []byte(`{"ref":"refs/heads/main","repository":{"full_name":"sidumandr/cisid","clone_url":"https://github.com/sidumandr/cisid.git"}}`),
			wantStatus: http.StatusBadRequest,
			wantBody:   webhookResponse{Status: "error", Reason: "invalid push payload"},
		},
		{
			name:       "missing repository is rejected",
			event:      "push",
			body:       []byte(`{"ref":"refs/heads/main","after":"6113728f27ae82c7b1a177c8d03f9e96e0adf246"}`),
			wantStatus: http.StatusBadRequest,
			wantBody:   webhookResponse{Status: "error", Reason: "invalid push payload"},
		},
		{
			name:  "store failure returns 500",
			event: "push",
			body:  pushBody,
			setup: func(m *mockJobCreator) {
				m.On("QueueJob", mock.Anything, mock.AnythingOfType("models.Job"), mock.Anything, mock.Anything).
					Return(int64(0), errStore).Once()
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   webhookResponse{Status: "error", Reason: "could not queue job"},
		},
		{
			name:       "GET is not allowed",
			method:     http.MethodGet,
			event:      "push",
			body:       pushBody,
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   webhookResponse{Status: "error", Reason: "method not allowed"},
		},
		{
			name:       "oversized body is rejected",
			event:      "push",
			body:       bytes.Repeat([]byte("a"), maxPayloadBytes+1),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   webhookResponse{Status: "error", Reason: "payload too large"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockJobCreator{}
			if tt.setup != nil {
				tt.setup(store)
			}
			h := newTestHandler(t, store)

			method := tt.method
			if method == "" {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, "/webhooks/github", bytes.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-GitHub-Event", tt.event)
			req.Header.Set("X-GitHub-Delivery", "72d3162e-cc78-11e3-81ab-4c9367dc0958")
			switch tt.signature {
			case "":
				req.Header.Set("X-Hub-Signature-256", sign(testSecret, tt.body))
			case "-":
				// no signature header
			default:
				req.Header.Set("X-Hub-Signature-256", tt.signature)
			}

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			var got webhookResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.Equal(t, tt.wantBody, got)

			store.AssertExpectations(t)
			if tt.setup == nil {
				store.AssertNotCalled(t, "QueueJob", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			}
		})
	}
}

func TestWebhookHandlerPassesRequestContext(t *testing.T) {
	type ctxKey struct{}
	body := loadPayload(t, "push.json")

	store := &mockJobCreator{}
	store.On("QueueJob",
		mock.MatchedBy(func(ctx context.Context) bool { return ctx.Value(ctxKey{}) == "req-ctx" }),
		mock.AnythingOfType("models.Job"), mock.Anything, mock.Anything,
	).Return(int64(1), nil).Once()
	h := newTestHandler(t, store)

	ctx := context.WithValue(context.Background(), ctxKey{}, "req-ctx")
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sign(testSecret, body))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	store.AssertExpectations(t)
}

// TestWebhookStoresRequestTrace checks that the queued job remembers the
// request's span, so its dispatch joins the trace, and the span describes
// the push.
func TestWebhookStoresRequestTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("test").Start(context.Background(), "POST /webhooks/github")
	sc := span.SpanContext()
	want := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"

	body := loadPayload(t, "push.json")
	store := &mockJobCreator{}
	store.On("QueueJob", mock.Anything, mock.MatchedBy(func(j models.Job) bool { return j.TraceParent == want }),
		mock.Anything, mock.Anything).Return(int64(5), nil).Once()
	h := newTestHandler(t, store)

	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sign(testSecret, body))
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	span.End()

	assert.Equal(t, http.StatusAccepted, resp.Code)
	store.AssertExpectations(t)
	attrs := rec.Ended()[0].Attributes()
	assert.Contains(t, attrs, telemetry.AttrJobID.Int64(5))
	assert.Contains(t, attrs, telemetry.AttrRepo.String("sidumandr/cisid"))
}

func signedPush(t *testing.T, h http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "d-1")
	req.Header.Set("X-Hub-Signature-256", sign(testSecret, body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebhookDeduplicatesBySignedBody(t *testing.T) {
	body := loadPayload(t, "push.json")
	sum := sha256.Sum256(body)
	store := &mockJobCreator{}
	store.On("QueueJob", mock.Anything, mock.AnythingOfType("models.Job"), hex.EncodeToString(sum[:]), "d-1").
		Return(int64(1), nil).Once()
	store.On("QueueJob", mock.Anything, mock.AnythingOfType("models.Job"), hex.EncodeToString(sum[:]), "d-1").
		Return(int64(0), fmt.Errorf("x: %w", db.ErrDuplicateDelivery)).Once()
	h := newTestHandler(t, store)

	assert.Equal(t, http.StatusAccepted, signedPush(t, h, body).Code)

	rec := signedPush(t, h, body)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ignored","reason":"duplicate delivery"}`, rec.Body.String())
	store.AssertExpectations(t)
}

func TestWebhookIgnoresStalePushes(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	withPushedAt := func(at time.Time) []byte {
		return bytes.Replace(loadPayload(t, "push.json"), []byte(`"full_name": "sidumandr/cisid",`),
			[]byte(fmt.Sprintf(`"full_name": "sidumandr/cisid", "pushed_at": %d,`, at.Unix())), 1)
	}

	store := &mockJobCreator{}
	store.On("QueueJob", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(int64(7), nil).Once()
	h := newTestHandler(t, store)
	h.now = func() time.Time { return now }

	assert.Equal(t, http.StatusAccepted, signedPush(t, h, withPushedAt(now.Add(-time.Hour))).Code)

	rec := signedPush(t, h, withPushedAt(now.Add(-8*24*time.Hour)))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ignored","reason":"stale delivery"}`, rec.Body.String(),
		"a week-old signed payload is a replay")
	store.AssertExpectations(t)
}

func TestWebhookCountsOutcomes(t *testing.T) {
	queued := metrics.Webhooks.WithLabelValues("queued")
	badSig := metrics.Webhooks.WithLabelValues("invalid_signature")
	q0, b0 := testutil.ToFloat64(queued), testutil.ToFloat64(badSig)

	store := &mockJobCreator{}
	store.On("QueueJob", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(int64(1), nil).Once()
	h := newTestHandler(t, store)
	body := loadPayload(t, "push.json")
	signedPush(t, h, body)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, q0+1, testutil.ToFloat64(queued))
	assert.Equal(t, b0+1, testutil.ToFloat64(badSig))
}
