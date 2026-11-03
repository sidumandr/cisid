package charttest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadToken(t *testing.T) {
	m := render(t, with(with(with(required(),
		"secrets.readToken", "read-token-0123456789"),
		"metrics.serviceMonitor.enabled", true),
		"metrics.serviceMonitor.authorize", true))

	assert.Equal(t, "read-token-0123456789", string(m.secrets["ci-cisid"].Data["read-token"]))
	c := container(t, m.server(t).Spec.Template.Spec, "server")
	ref := env(c, "CISID_READ_TOKEN").ValueFrom.SecretKeyRef
	assert.Equal(t, "read-token", ref.Key)
	assert.True(t, *ref.Optional)

	monitor := m.others["ServiceMonitor/ci-cisid-server"]
	require.NotNil(t, monitor)
	endpoint := monitor["spec"].(map[string]any)["endpoints"].([]any)[0].(map[string]any)
	auth := endpoint["authorization"].(map[string]any)
	assert.Equal(t, "Bearer", auth["type"])
	assert.Equal(t, map[string]any{"name": "ci-cisid", "key": "read-token"}, auth["credentials"])

	_, err := renderFiles(with(required(), "secrets.readToken", "short"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secrets.readToken must be at least 16")
}
