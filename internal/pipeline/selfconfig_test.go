package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The repository's own pipeline must always be valid.
func TestRepositoryConfigIsValid(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ConfigFile))
	require.NoError(t, err)
	_, err = ParseConfig(data)
	require.NoError(t, err)
}
