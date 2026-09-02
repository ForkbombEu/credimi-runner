package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControllerClientAuthentication(t *testing.T) {
	for _, tc := range []struct{ name, token, want string }{{"none", "", ""}, {"bearer", "secret", "Bearer secret"}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != tc.want {
					t.Fatalf("authorization=%q", got)
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			client := &controllerClient{baseURL: server.URL, token: tc.token, client: server.Client()}
			var response map[string]bool
			if err := client.postJSON(context.Background(), "/runtime", &response); err != nil || !response["ok"] {
				t.Fatalf("response=%v err=%v", response, err)
			}
			if err := client.getJSON(context.Background(), "/operations/1", &response); err != nil {
				t.Fatal(err)
			}
		})
	}
}
