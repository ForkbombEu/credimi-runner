package application

import "testing"

func TestIsLoopbackHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{{"127.0.0.1", true}, {"127.0.0.2", true}, {"::1", true}, {"localhost", true}, {"LOCALHOST", true}, {"0.0.0.0", false}, {"::", false}, {"192.168.1.5", false}, {"example.com", false}} {
		if got := isLoopbackHost(tc.host); got != tc.want {
			t.Errorf("%q: got %v want %v", tc.host, got, tc.want)
		}
	}
}
