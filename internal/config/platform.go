package config

import "runtime"

var runtimeGOOS = func() string { return runtime.GOOS }
