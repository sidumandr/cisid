package models

import "time"

// Notification kinds: where a notification goes.
const (
	// NotifySlack posts to a Slack incoming webhook, or to a chat that
	// accepts its messages (Mattermost, Rocket.Chat, Discord's /slack URL).
	NotifySlack = "slack"
	// NotifyWebhook posts the result as JSON to any URL.
	NotifyWebhook = "webhook"
	// NotifyEmail sends an email through the server's SMTP relay.
	NotifyEmail = "email"
)

// Results a notification can be sent for. A failed job is also "broken"
// when the build before it passed (or there was none); a passed job is
// also "fixed" when the build before it failed.
const (
	ResultFailed = "failed"
	ResultBroken = "broken"
	ResultFixed  = "fixed"
	ResultPassed = "passed"
)

// Notification announces a repository's build results. Where it goes (a
// URL, which is a credential, or addresses) is stored sealed; Target only
// says enough to recognize it.
type Notification struct {
	ID   int64  `json:"-"`
	Repo string `json:"repo"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Target is the URL's host, or the email addresses.
	Target string `json:"target"`
	// On lists the results it is sent for, of the Result constants.
	On []string `json:"on"`
	// Branches limits it to builds of these branches (glob patterns);
	// empty means every branch.
	Branches []string `json:"branches,omitempty"`
	// PullRequests sends it for pull request builds too, matching
	// Branches against the branch they merge into.
	PullRequests bool `json:"pull_requests"`
	// LastSentAt is when it was last sent; LastError why the last attempt
	// failed, empty after a good one.
	LastSentAt *time.Time `json:"last_sent_at,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
