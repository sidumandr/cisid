package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// ConfigFile is the pipeline definition read from the repository root.
const ConfigFile = ".cisid.yml"

const (
	// DefaultStepTimeout applies to steps without a timeout.
	DefaultStepTimeout = time.Hour
	maxStepTimeout     = 24 * time.Hour
	maxConfigBytes     = 64 << 10
	maxSteps           = 50
)

var (
	// ErrInvalidConfig is returned for a .cisid.yml that cannot be run.
	ErrInvalidConfig = errors.New("invalid " + ConfigFile)

	stepNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	envNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Config is a parsed .cisid.yml:
//
//	steps:
//	  - name: test
//	    image: golang:1.27
//	    commands:
//	      - go test ./...
//	    environment:
//	      CGO_ENABLED: 0
//	    timeout: 10m
//	    when:
//	      branch: [main, "release/*"]
type Config struct {
	Steps []Step
}

// Step is one step of a pipeline. Its commands run in order in a shell in
// Image, with the repository checked out in the working directory.
type Step struct {
	Name        string
	Image       string
	Commands    []string
	Environment map[string]string
	Timeout     time.Duration
	// Branches limits the step to pushes to matching branches; nil runs
	// it for every branch.
	Branches *branchMatcher
}

// RunsOn reports whether the step runs for a push to branch.
func (s Step) RunsOn(branch string) bool {
	return s.Branches.Matches(branch)
}

type rawConfig struct {
	Steps []rawStep `yaml:"steps"`
}

type rawStep struct {
	Name        string            `yaml:"name"`
	Image       string            `yaml:"image"`
	Commands    []string          `yaml:"commands"`
	Environment map[string]string `yaml:"environment"`
	Timeout     string            `yaml:"timeout"`
	When        rawWhen           `yaml:"when"`
}

// ParseConfig parses and validates a .cisid.yml, reporting every problem
// at once. Unknown fields are errors, so typos do not go unnoticed.
func ParseConfig(data []byte) (Config, error) {
	if len(data) > maxConfigBytes {
		return Config{}, fmt.Errorf("%w: file is over %d bytes", ErrInvalidConfig, maxConfigBytes)
	}

	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%w: parse: %w", ErrInvalidConfig, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%w: must be a single YAML document", ErrInvalidConfig)
	}

	switch {
	case len(raw.Steps) == 0:
		return Config{}, fmt.Errorf("%w: no steps", ErrInvalidConfig)
	case len(raw.Steps) > maxSteps:
		return Config{}, fmt.Errorf("%w: at most %d steps are allowed, got %d", ErrInvalidConfig, maxSteps, len(raw.Steps))
	}

	var (
		cfg      Config
		problems []error
		seen     = map[string]bool{}
	)
	for i, rs := range raw.Steps {
		bad := func(format string, args ...any) {
			problems = append(problems, fmt.Errorf("steps[%d]: "+format, append([]any{i}, args...)...))
		}

		switch {
		case rs.Name == "":
			bad("name is required")
		case rs.Name == cloneStepName:
			bad("name %q is reserved", rs.Name)
		case !stepNameRe.MatchString(rs.Name):
			bad("name %q may only contain letters, digits, '.', '_' and '-'", rs.Name)
		case seen[rs.Name]:
			bad("name %q is used twice", rs.Name)
		}
		seen[rs.Name] = true

		switch {
		case rs.Image == "":
			bad("image is required")
		case strings.ContainsAny(rs.Image, " \t\r\n"):
			bad("image %q contains whitespace", rs.Image)
		}

		if len(rs.Commands) == 0 {
			bad("commands are required")
		}
		for j, c := range rs.Commands {
			if strings.TrimSpace(c) == "" {
				bad("commands[%d] is empty", j)
			}
		}

		for k := range rs.Environment {
			switch {
			case !envNameRe.MatchString(k):
				bad("environment name %q is invalid", k)
			case k == "CI" || k == "CISID" || strings.HasPrefix(k, "CISID_"):
				bad("environment %q is reserved", k)
			}
		}

		timeout := DefaultStepTimeout
		if rs.Timeout != "" {
			d, err := time.ParseDuration(rs.Timeout)
			switch {
			case err != nil:
				bad("timeout %q: %w", rs.Timeout, err)
			case d <= 0:
				bad("timeout %s must be positive", d)
			case d > maxStepTimeout:
				bad("timeout %s is over %s", d, maxStepTimeout)
			default:
				timeout = d
			}
		}

		var branches *branchMatcher
		if rs.When.Branch != nil {
			m, err := newBranchMatcher(rs.When.Branch)
			if err != nil {
				bad("when.branch: %w", err)
			}
			branches = m
		}

		cfg.Steps = append(cfg.Steps, Step{
			Name:        rs.Name,
			Image:       rs.Image,
			Commands:    rs.Commands,
			Environment: rs.Environment,
			Timeout:     timeout,
			Branches:    branches,
		})
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalidConfig, errors.Join(problems...))
	}
	return cfg, nil
}
