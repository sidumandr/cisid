package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/sidumandr/cisid/internal/status"
	"github.com/sidumandr/cisid/pkg/models"
)

// ResultTest marks the message a notification's test sends.
const ResultTest = "test"

// Message is one announcement.
type Message struct {
	// Result is what happened: one of the models.Result constants, or
	// ResultTest.
	Result string
	Repo   string
	// Title says it in one line, Detail adds a sentence.
	Title, Detail string
	// Facts identify the build, e.g. "Build #42", "commit 6113728", "push".
	Facts []string
	// URL is the job's page (the repository's for a test); empty without
	// CISID_PUBLIC_URL.
	URL string
	// Job is the job announced; nil for a test.
	Job *models.Job
}

var headline = map[string]string{
	models.ResultFailed: "failed again",
	models.ResultBroken: "is broken",
	models.ResultFixed:  "is fixed",
	models.ResultPassed: "passed",
}

// jobMessage announces job, whose most telling result is result; compared
// says whether it was compared with the build before.
func jobMessage(job models.Job, result string, compared bool, url string) Message {
	where := job.Branch
	if job.Event == models.EventPullRequest {
		where = fmt.Sprintf("pull request #%d (%s → %s)", job.PullRequest, job.Branch, job.TargetBranch)
	}
	if label := models.MatrixLabel(job.Matrix); label != "" {
		where += " (" + label + ")"
	}
	verb := headline[result]
	if result == models.ResultFailed && !compared {
		// Without the build before, a failure is just a failure.
		verb = "failed"
	}

	detail := status.Describe(job).Description
	if job.Status == models.JobStatusFailed && job.StartedAt != nil && job.FinishedAt != nil {
		detail += " after " + job.FinishedAt.Sub(*job.StartedAt).Round(time.Second).String()
	}
	trigger := job.Event
	switch job.Event {
	case models.EventPullRequest:
		trigger = "pull request"
	case models.EventManual:
		trigger = "manual build"
	case models.EventSchedule:
		trigger = "scheduled build"
	}
	j := job
	return Message{
		Result: result,
		Repo:   job.Repo,
		Title:  fmt.Sprintf("%s: %s %s", job.Repo, where, verb),
		Detail: detail + ".",
		Facts:  []string{fmt.Sprintf("Build #%d", job.ID), "commit " + shortSHA(job.CommitSHA), trigger},
		URL:    url,
		Job:    &j,
	}
}

// testMessage is what a notification's test sends.
func testMessage(repo, url string) Message {
	return Message{
		Result: ResultTest,
		Repo:   repo,
		Title:  repo + ": test notification",
		Detail: "This is a test from cisid. Build results of " + repo + " will arrive here.",
		URL:    url,
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Subject is the message's email subject.
func (m Message) Subject() string {
	return "[cisid] " + m.Title
}

// Plain is the message as plain text.
func (m Message) Plain() string {
	var b strings.Builder
	b.WriteString(m.Title + "\n\n" + m.Detail + "\n")
	if len(m.Facts) > 0 {
		b.WriteString(strings.Join(m.Facts, " · ") + "\n")
	}
	if m.URL != "" {
		b.WriteString("\n" + m.URL + "\n")
	}
	return b.String()
}

// slackEscaper escapes the characters Slack's message format gives a
// meaning, so names cannot turn into links or mentions.
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Slack is the message in Slack's mrkdwn format.
func (m Message) Slack() string {
	esc := slackEscaper.Replace
	text := "*" + esc(m.Title) + "*\n" + esc(m.Detail)
	facts := make([]string, len(m.Facts))
	for i, f := range m.Facts {
		facts[i] = esc(f)
	}
	if len(facts) > 0 && m.URL != "" {
		// The build links to its page.
		facts[0] = "<" + m.URL + "|" + facts[0] + ">"
	}
	if len(facts) > 0 {
		text += "\n" + strings.Join(facts, " · ")
	} else if m.URL != "" {
		text += "\n<" + m.URL + ">"
	}
	return text
}
