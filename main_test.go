package main

import (
	"os"
	"testing"
)

func TestMain(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })
	os.Args = []string{"credimi-runner", "--help"}

	main()
}
