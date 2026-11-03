package pipeline

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/pkg/models"
)

// ConfigFile is the pipeline definition read from the repository root.
const ConfigFile = ".cisid.yml"

const (
	// DefaultStepTimeout applies to steps without a timeout.
	DefaultStepTimeout = time.Hour
	maxStepTimeout     = 24 * time.Hour
	maxConfigBytes     = 64 << 10
	maxSteps           = 50
	maxServices        = 10
)

var (
	// ErrInvalidConfig is returned for a .cisid.yml that cannot be run.
	ErrInvalidConfig = errors.New("invalid " + ConfigFile)

	stepNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	// Service names become host names, so they must be DNS labels.
	serviceNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	envNameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
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
//	    secrets: [DEPLOY_TOKEN]
//
//	services:
//	  - name: postgres
//	    image: postgres:18-alpine
//	    environment:
//	      POSTGRES_PASSWORD: test
//
//	matrix:
//	  GO_VERSION: ["1.26", "1.27"]
type Config struct {
	Steps    []Step
	Services []Service
	// Matrix runs the pipeline once per combination of its values, each
	// as a separate job; steps and services see each axis as an
	// environment variable, and images and environment values may use
	// ${AXIS}.
	Matrix Matrix
}

// Service is a container started before the steps and stopped after them.
// Steps reach it at host Name.
type Service struct {
	Name        string
	Image       string
	Environment map[string]string
	Command     []string
}

// Step is one step of a pipeline. Its commands run in order in a shell in
// Image, with the repository checked out in the working directory.
type Step struct {
	Name        string
	Image       string
	Commands    []string
	Environment map[string]string
	Timeout     time.Duration
	// Branches limits the step to jobs on matching branches (a pull
	// request's own branch); nil runs it for every branch.
	Branches *branchMatcher
	// Events limits the step to jobs triggered by these events; nil runs
	// it for every event.
	Events []string
	// Secrets names the repository secrets the step gets as environment
	// variables.
	Secrets []string
}

// RunsFor reports whether the step runs for job, by its event and branch.
func (s Step) RunsFor(job models.Job) bool {
	if s.Events != nil && !slices.Contains(s.Events, cmp.Or(job.Event, models.EventPush)) {
		return false
	}
	return s.Branches.Matches(job.Branch)
}

// events are the values when.event accepts.
var events = []string{models.EventPush, models.EventPullRequest}

type rawConfig struct {
	Steps    []rawStep             `yaml:"steps"`
	Services []rawService          `yaml:"services"`
	Matrix   map[string]stringList `yaml:"matrix"`
}

type rawService struct {
	Name        string            `yaml:"name"`
	Image       string            `yaml:"image"`
	Environment map[string]string `yaml:"environment"`
	Command     []string          `yaml:"command"`
}

type rawStep struct {
	Name        string            `yaml:"name"`
	Image       string            `yaml:"image"`
	Commands    []string          `yaml:"commands"`
	Environment map[string]string `yaml:"environment"`
	Timeout     string            `yaml:"timeout"`
	When        rawWhen           `yaml:"when"`
	Secrets     []string          `yaml:"secrets"`
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
	if len(raw.Matrix) > 0 {
		cfg.Matrix = Matrix{}
		for axis, values := range raw.Matrix {
			cfg.Matrix[axis] = values
		}
		problems = append(problems, cfg.Matrix.validate()...)
	}
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
		for _, name := range cfg.Matrix.undefinedVars(rs.Image) {
			bad("image refers to ${%s}, which is not a matrix axis", name)
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
			case models.ReservedEnv(k):
				bad("environment %q is reserved", k)
			}
			if _, axis := cfg.Matrix[k]; axis {
				bad("environment %q is also a matrix axis", k)
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

		seenSecret := map[string]bool{}
		for _, name := range rs.Secrets {
			switch err := secrets.ValidateName(name); {
			case err != nil:
				bad("secrets: %w", err)
			case seenSecret[name]:
				bad("secrets: %q is listed twice", name)
			default:
				if _, clash := rs.Environment[name]; clash {
					bad("secrets: %q is also set in environment", name)
				}
			}
			seenSecret[name] = true
		}

		var branches *branchMatcher
		if rs.When.Branch != nil {
			m, err := newBranchMatcher(rs.When.Branch)
			if err != nil {
				bad("when.branch: %w", err)
			}
			branches = m
		}
		for _, e := range rs.When.Event {
			if !slices.Contains(events, e) {
				bad("when.event %q is not one of %s", e, strings.Join(events, ", "))
			}
		}

		cfg.Steps = append(cfg.Steps, Step{
			Name:        rs.Name,
			Image:       rs.Image,
			Commands:    rs.Commands,
			Environment: rs.Environment,
			Timeout:     timeout,
			Branches:    branches,
			Events:      rs.When.Event,
			Secrets:     rs.Secrets,
		})
	}

	cfg.Services, problems = parseServices(raw.Services, cfg.Matrix, problems)

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalidConfig, errors.Join(problems...))
	}
	return cfg, nil
}

func parseServices(raw []rawService, matrix Matrix, problems []error) ([]Service, []error) {
	if len(raw) > maxServices {
		return nil, append(problems, fmt.Errorf("at most %d services are allowed, got %d", maxServices, len(raw)))
	}
	var (
		services []Service
		seen     = map[string]bool{}
	)
	for i, rs := range raw {
		bad := func(format string, args ...any) {
			problems = append(problems, fmt.Errorf("services[%d]: "+format, append([]any{i}, args...)...))
		}
		switch {
		case rs.Name == "":
			bad("name is required")
		case !serviceNameRe.MatchString(rs.Name):
			bad("name %q must be a host name: lowercase letters, digits and '-'", rs.Name)
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
		for _, name := range matrix.undefinedVars(rs.Image) {
			bad("image refers to ${%s}, which is not a matrix axis", name)
		}
		for k := range rs.Environment {
			if !envNameRe.MatchString(k) {
				bad("environment name %q is invalid", k)
			}
		}
		services = append(services, Service(rs))
	}
	return services, problems
}

func reservedEnv(name string) bool { return models.ReservedEnv(name) }
