package pipeline

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// stringList decodes either a single string or a list of strings.
type stringList []string

func (l *stringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = stringList{n.Value}
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*l = list
	return nil
}

type rawWhen struct {
	Branch stringList `yaml:"branch"`
	Event  stringList `yaml:"event"`
}

// branchMatcher matches branch names against glob patterns: "*" matches
// within one path segment, "**" across segments, "?" one character.
type branchMatcher struct {
	patterns []string
	res      []*regexp.Regexp
}

func newBranchMatcher(patterns []string) (*branchMatcher, error) {
	m := &branchMatcher{patterns: patterns}
	for _, p := range patterns {
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("branch pattern is empty")
		}
		m.res = append(m.res, globToRegexp(p))
	}
	return m, nil
}

// Matches reports whether branch matches any pattern. A nil matcher
// matches every branch.
func (m *branchMatcher) Matches(branch string) bool {
	if m == nil {
		return true
	}
	for _, re := range m.res {
		if re.MatchString(branch) {
			return true
		}
	}
	return false
}

// MatchBranch reports whether branch matches any of the glob patterns, as
// when.branch matches them. No patterns match nothing.
func MatchBranch(patterns []string, branch string) bool {
	m, err := newBranchMatcher(patterns)
	return err == nil && m.Matches(branch)
}

func globToRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
