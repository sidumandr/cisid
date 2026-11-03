package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func masked(t *testing.T, secrets map[string]string, writes ...string) string {
	t.Helper()
	var out strings.Builder
	w := newMaskWriter(&out, secrets)
	for _, s := range writes {
		n, err := w.Write([]byte(s))
		require.NoError(t, err)
		require.Equal(t, len(s), n)
	}
	require.NoError(t, w.Flush())
	return out.String()
}

func TestMaskWriter(t *testing.T) {
	secrets := map[string]string{"TOKEN": "abc123", "PASSWORD": "hunter22"}

	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{"whole value", []string{"token=abc123\n"}, "token=***\n"},
		{"several values", []string{"abc123 hunter22 abc123\n"}, "*** *** ***\n"},
		{"split across writes", []string{"token=ab", "c1", "23 ok\n"}, "token=*** ok\n"},
		{"split at the end", []string{"token=abc12", "3"}, "token=***"},
		{"prefix that is not a value", []string{"abc12", "4\n"}, "abc124\n"},
		{"prefix at the very end", []string{"abc"}, "abc"},
		{"no secrets", []string{"plain\n"}, "plain\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, masked(t, secrets, tt.writes...))
		})
	}
}

func TestMaskWriterByteByByte(t *testing.T) {
	text := "user=admin password=hunter22 token=abc123 end\n"
	var writes []string
	for i := range len(text) {
		writes = append(writes, text[i:i+1])
	}

	got := masked(t, map[string]string{"TOKEN": "abc123", "PASSWORD": "hunter22"}, writes...)

	assert.Equal(t, "user=admin password=*** token=*** end\n", got)
}

func TestMaskWriterValueContainingAnother(t *testing.T) {
	got := masked(t, map[string]string{"A": "secret", "B": "secret-long"}, "x secret-long y secret\n")
	assert.Equal(t, "x *** y ***\n", got)
}

func TestMaskWriterMultiLineValue(t *testing.T) {
	key := "-----BEGIN KEY-----\nAAAABBBBCCCC\n-----END KEY-----"
	got := masked(t, map[string]string{"KEY": key}, "+ echo AAAABBBBCCCC\n")
	assert.Equal(t, "+ echo ***\n", got, "each line of a multi-line value is masked")
}

func TestMaskWriterSkipsTinyValues(t *testing.T) {
	got := masked(t, map[string]string{"FLAG": "1", "EMPTY": "", "SHORT": "ab"}, "a1b ab 1\n")
	assert.Equal(t, "a1b ab 1\n", got, "values under 3 characters would garble the log")
}
