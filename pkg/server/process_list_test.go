package server

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunningProcessNames(t *testing.T) {
	procs := []*Process{
		{Name: "zeta", Running: true},
		{Name: "alpha", Running: false},
		{Name: "beta", Running: true},
	}

	names := runningProcessNames(procs)

	require.Equal(t, []string{"zeta", "beta"}, names)
}
