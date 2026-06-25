package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCredimiClient(t *testing.T) {
	var registerPayload RegisterRunnerRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations/my":
			_ = json.NewEncoder(w).Encode(Organization{Name: "Acme", Namespace: "acme"})
		case "/api/mobile-runner/preview-id":
			_ = json.NewEncoder(w).Encode(RunnerPreview{Organization: "acme", RunnerID: "acme/runner"})
		case "/api/mobile-runner":
			_ = json.NewDecoder(r.Body).Decode(&registerPayload)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	org, err := client.MyOrganization(context.Background())
	if err != nil || org.Namespace != "acme" {
		t.Fatalf("organization = %#v err=%v", org, err)
	}
	preview, err := client.PreviewRunnerID(context.Background(), "Runner", "acme")
	if err != nil || preview.RunnerID != "acme/runner" {
		t.Fatalf("preview = %#v err=%v", preview, err)
	}
	err = client.RegisterMobileRunner(context.Background(), RegisterRunnerRequest{
		RunnerID: "acme/runner", Type: "android_phone", Serial: "device-1", Port: "443",
	})
	if err != nil {
		t.Fatal(err)
	}
	if registerPayload.Type != "android_phone" || registerPayload.Serial != "device-1" || registerPayload.Port != "443" {
		t.Fatalf("payload = %#v", registerPayload)
	}
}
