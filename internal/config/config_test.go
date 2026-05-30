package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flowcatalyst/flowcatalyst-go/internal/config"
)

func TestDefaultsAreApplied(t *testing.T) {
	c, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, uint16(8080), c.HTTP.APIPort)
	assert.Equal(t, uint16(9090), c.HTTP.MetricsPort)
	assert.Equal(t, "0.0.0.0", c.HTTP.BindAddr)
	assert.Equal(t, "postgres", c.Outbox.DBType)
}

func TestFileOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[http]
api_port = 4000

[db]
url = "postgres://override"
`), 0o644))

	c, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, uint16(4000), c.HTTP.APIPort)
	assert.Equal(t, "postgres://override", c.DB.URL)
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.toml")
	require.NoError(t, os.WriteFile(path, []byte(`[http]
api_port = 4000
`), 0o644))

	t.Setenv("FC_API_PORT", "5000")

	c, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, uint16(5000), c.HTTP.APIPort)
}

func TestMissingFileIsOK(t *testing.T) {
	c, err := config.Load("/does/not/exist.toml")
	require.NoError(t, err)
	assert.NotNil(t, c)
}
