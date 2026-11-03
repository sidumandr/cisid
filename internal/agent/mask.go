package agent

import (
	"cmp"
	"io"
	"slices"
	"strings"
)

const (
	// mask replaces secret values in step output.
	mask = "***"
	// minMaskLen skips values too short to mask without garbling the log.
	minMaskLen = 3
)

// flushWriter is a step output writer that may hold back data until Flush.
type flushWriter interface {
	io.Writer
	Flush() error
}

type nopFlusher struct{ io.Writer }

func (nopFlusher) Flush() error { return nil }

// maskWriter replaces secret values with *** before passing output on. A
// value split across writes is still masked: the end of a write that could
// be the start of a value is held back until the next write or Flush.
type maskWriter struct {
	w        io.Writer
	replacer *strings.Replacer
	values   []string
	held     string
}

// newMaskWriter masks values (and, for multi-line values, each of their
// lines) in what is written to w.
func newMaskWriter(w io.Writer, secrets map[string]string) flushWriter {
	var values []string
	for _, v := range secrets {
		values = append(values, v)
		if strings.Contains(v, "\n") {
			values = append(values, strings.Split(v, "\n")...)
		}
	}
	values = slices.DeleteFunc(values, func(v string) bool { return len(strings.TrimSpace(v)) < minMaskLen })
	if len(values) == 0 {
		return nopFlusher{w}
	}
	// Longest first, so a value containing another is masked whole.
	slices.SortFunc(values, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	values = slices.Compact(values)

	pairs := make([]string, 0, 2*len(values))
	for _, v := range values {
		pairs = append(pairs, v, mask)
	}
	return &maskWriter{w: w, replacer: strings.NewReplacer(pairs...), values: values}
}

func (m *maskWriter) Write(p []byte) (int, error) {
	s := m.replacer.Replace(m.held + string(p))
	keep := m.partialSuffix(s)
	m.held = s[len(s)-keep:]
	if out := s[:len(s)-keep]; out != "" {
		if _, err := io.WriteString(m.w, out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush writes what was held back; call it when the output ends.
func (m *maskWriter) Flush() error {
	if m.held == "" {
		return nil
	}
	s := m.replacer.Replace(m.held)
	m.held = ""
	_, err := io.WriteString(m.w, s)
	return err
}

// partialSuffix returns the length of the longest end of s that is the
// beginning, but not the whole, of a secret value.
func (m *maskWriter) partialSuffix(s string) int {
	for k := min(len(s), len(m.values[0])-1); k > 0; k-- {
		tail := s[len(s)-k:]
		for _, v := range m.values {
			if len(v) > k && strings.HasPrefix(v, tail) {
				return k
			}
		}
	}
	return 0
}
