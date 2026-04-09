package cmd

import (
	"bytes"
	"testing"

	"github.com/forkbombeu/credimi-runner/internal/buildinfo"
	"github.com/stretchr/testify/require"
)

func TestVersionCmdRunE(t *testing.T) {
	var output bytes.Buffer
	versionCmd.SetOut(&output)
	t.Cleanup(func() {
		versionCmd.SetOut(nil)
	})

	err := versionCmd.RunE(versionCmd, nil)
	require.NoError(t, err)
	require.Equal(t, buildinfo.String()+"\n", output.String())
}
