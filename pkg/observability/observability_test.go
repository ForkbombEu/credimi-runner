package observability

import "testing"

func TestOTLPSignalEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		base        string
		defaultPath string
		want        string
	}{
		{
			name:        "empty base uses default path",
			base:        "",
			defaultPath: "/v1/traces",
			want:        "/v1/traces",
		},
		{
			name:        "base url appends default path",
			base:        "http://127.0.0.1:4318",
			defaultPath: "/v1/logs",
			want:        "http://127.0.0.1:4318/v1/logs",
		},
		{
			name:        "base url with slash appends default path",
			base:        "http://127.0.0.1:4318/",
			defaultPath: "/v1/traces",
			want:        "http://127.0.0.1:4318/v1/traces",
		},
		{
			name:        "signal specific path is preserved",
			base:        "http://127.0.0.1:4318/custom/path",
			defaultPath: "/v1/metrics",
			want:        "http://127.0.0.1:4318/custom/path",
		},
		{
			name:        "host port value is preserved",
			base:        "127.0.0.1:4318",
			defaultPath: "/v1/logs",
			want:        "127.0.0.1:4318",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := otlpSignalEndpoint(tt.base, tt.defaultPath); got != tt.want {
				t.Fatalf("otlpSignalEndpoint(%q, %q) = %q, want %q", tt.base, tt.defaultPath, got, tt.want)
			}
		})
	}
}
