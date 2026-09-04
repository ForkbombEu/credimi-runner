package application

import "testing"

func TestDashboardHostPortDefaultsAndWildcard(t *testing.T) {
	for _, tc := range []struct {
		values     map[string]string
		host, port string
	}{
		{map[string]string{}, "127.0.0.1", "8051"},
		{map[string]string{"DASHBOARD_HOST": "0.0.0.0", "DASHBOARD_PORT": "9051"}, "127.0.0.1", "9051"},
		{map[string]string{"DASHBOARD_HOST": "::", "DASHBOARD_PORT": "9051"}, "127.0.0.1", "9051"},
		{map[string]string{"DASHBOARD_HOST": "192.0.2.5", "DASHBOARD_PORT": "9051"}, "192.0.2.5", "9051"},
	} {
		host, port := dashboardHostPort(tc.values)
		if host != tc.host || port != tc.port {
			t.Fatalf("got %s:%s, want %s:%s", host, port, tc.host, tc.port)
		}
	}
}
