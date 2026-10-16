package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthzsourceToken(t *testing.T) {
	t.Run("reports a missing ssh key", func(t *testing.T) {
		_, err := authzsource{path: filepath.Join(t.TempDir(), "eg")}.Token()
		require.ErrorContains(t, err, "unable to read ssh key")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("reports a malformed ssh key", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "eg")
		require.NoError(t, os.WriteFile(p, []byte("not a key"), 0600))

		_, err := authzsource{path: p}.Token()
		require.ErrorContains(t, err, "unable to parse ssh key")
	})
}
