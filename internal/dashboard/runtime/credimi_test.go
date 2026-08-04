package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCredimiClient(t *testing.T) {
	var registerPayload RegisterRunnerRequest
	var devicePayload RegisterDeviceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations/my":
			_ = json.NewEncoder(w).Encode(Organization{Name: "Acme", Namespace: "acme"})
		case "/api/mobile-runner/preview-id":
			_ = json.NewEncoder(w).Encode(RunnerPreview{Organization: "acme", RunnerID: "acme/runner"})
		case "/api/mobile-runner":
			_ = json.NewDecoder(r.Body).Decode(&registerPayload)
			w.WriteHeader(http.StatusOK)
		case "/api/mobile-device/preview-id":
			_ = json.NewEncoder(w).Encode(DevicePreview{RunnerID: "acme/runner", DeviceID: "acme/runner/pixel", CanonifiedName: "pixel"})
		case "/api/mobile-device":
			_ = json.NewDecoder(r.Body).Decode(&devicePayload)
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
	published := true
	err = client.RegisterMobileRunner(context.Background(), RegisterRunnerRequest{
		RunnerID: "acme/runner", Port: "443",
		Published: &published,
	})
	if err != nil {
		t.Fatal(err)
	}
	if registerPayload.Port != "443" || registerPayload.Published == nil || !*registerPayload.Published {
		t.Fatalf("payload = %#v", registerPayload)
	}
	device, err := client.PreviewDeviceID(context.Background(), "acme/runner", "Pixel", "acme")
	if err != nil || device.DeviceID != "acme/runner/pixel" {
		t.Fatalf("device preview = %#v err=%v", device, err)
	}
	err = client.RegisterMobileDevice(context.Background(), RegisterDeviceRequest{RunnerID: "acme/runner", DeviceID: "acme/runner/pixel", Name: "Pixel", Type: "android_phone", Serial: "usb-1"})
	if err != nil {
		t.Fatal(err)
	}
	if devicePayload.Serial != "usb-1" || devicePayload.DeviceID != "acme/runner/pixel" {
		t.Fatalf("device payload = %#v", devicePayload)
	}
}

func TestCredimiClientIncludesResponseBodyInErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"domain":"name","reason":"name is required"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	err := client.RegisterMobileRunner(context.Background(), RegisterRunnerRequest{
		RunnerID: "acme/runner",
		IP:       "https://runner.example",
	})
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("RegisterMobileRunner error = %v", err)
	}
}

func TestCredimiClientRejectsInvalidDeviceResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/mobile-device/preview-id" {
			_, _ = w.Write([]byte("not-json"))
			return
		}
		http.Error(w, "device rejected", http.StatusBadRequest)
	}))
	defer server.Close()
	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	if _, err := client.PreviewDeviceID(context.Background(), "acme/runner", "Pixel", "acme"); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("PreviewDeviceID error = %v", err)
	}
	if err := client.RegisterMobileDevice(context.Background(), RegisterDeviceRequest{RunnerID: "acme/runner", DeviceID: "acme/runner/pixel", Name: "Pixel", Type: "android_phone"}); err == nil || !strings.Contains(err.Error(), "device rejected") {
		t.Fatalf("RegisterMobileDevice error = %v", err)
	}
}

func TestCredimiClientDetectsRunnerNameConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":{"reason":"runner_name_conflict"},"message":"name does not match the existing runner_id"}`))
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	err := client.RegisterMobileRunner(context.Background(), RegisterRunnerRequest{
		RunnerID: "acme/runner",
		Name:     "runner",
		IP:       "https://runner.example",
	})
	if !IsRunnerNameConflict(err) {
		t.Fatalf("IsRunnerNameConflict(%v) = false", err)
	}
}

func TestCredimiClientRetriesRegistrationWithStoredRunnerName(t *testing.T) {
	var attempts int
	var names []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/mobile-runner":
			attempts++
			var payload RegisterRunnerRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			names = append(names, payload.Name)
			if attempts == 1 {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":{"reason":"runner_name_conflict"},"message":"name does not match the existing runner_id"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/api/mobile-runners":
			_ = json.NewEncoder(w).Encode(MobileRunnerListResponse{
				Runners: []MobileRunnerListItem{{Name: "My Phone", Path: "acme/my-phone"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	err := client.RegisterMobileRunnerResolvingName(context.Background(), RegisterRunnerRequest{
		RunnerID: "acme/my-phone",
		Name:     "my-phone",
		IP:       "https://new.example.trycloudflare.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || strings.Join(names, ",") != "my-phone,My Phone" {
		t.Fatalf("attempts=%d names=%v", attempts, names)
	}
}

func TestCredimiClientRetriesRegistrationEvenWhenStoredNameLooksEqual(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/mobile-runner":
			attempts++
			if attempts == 1 {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"error":{"reason":"runner_name_conflict"},"message":"name does not match the existing runner_id"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/api/mobile-runners":
			_ = json.NewEncoder(w).Encode(MobileRunnerListResponse{
				Runners: []MobileRunnerListItem{{Name: "Test-Runner-Dashboard", Path: "filippo-s-organization/test-runner-dashboard"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	err := client.RegisterMobileRunnerResolvingName(context.Background(), RegisterRunnerRequest{
		RunnerID: "filippo-s-organization/test-runner-dashboard",
		Name:     "Test-Runner-Dashboard",
		IP:       "https://new.example.trycloudflare.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestCredimiHelpers(t *testing.T) {
	err := &CredimiStatusError{Prefix: "preview failed", Status: "409 Conflict", StatusCode: http.StatusConflict, Body: "runner_name_conflict"}
	if !IsRunnerNameConflict(err) {
		t.Fatal("expected runner name conflict detection")
	}
	if IsRunnerNameConflict(errors.New("other")) {
		t.Fatal("unexpected conflict detection")
	}

	client := &CredimiClient{}
	if client.httpClient() != http.DefaultClient {
		t.Fatal("expected default client fallback")
	}

	resp := &http.Response{Status: "502 Bad Gateway", StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("upstream bad"))}
	if got := credimiResponseError("lookup failed", resp).Error(); !strings.Contains(got, "upstream bad") {
		t.Fatalf("credimiResponseError = %q", got)
	}
}

func TestCredimiClientPreviewFallbackAndVisibleRunnerLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/mobile-runner/preview-id":
			_, _ = w.Write([]byte(`{}`))
		case "/api/mobile-runners":
			_, _ = w.Write([]byte(`{"runners":[{"name":"Saved Runner","path":"/acme/runner"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	preview, err := client.PreviewRunnerID(context.Background(), "Runner Name", "acme")
	if err != nil || preview.RunnerID != "acme/runner-name" || preview.Organization != "acme" {
		t.Fatalf("preview fallback = %#v, %v", preview, err)
	}
	name, err := client.MobileRunnerName(context.Background(), "/acme/runner")
	if err != nil || name != "Saved Runner" {
		t.Fatalf("visible runner name = %q, %v", name, err)
	}
}

func TestCredimiClientReportsLookupFailuresWithResponseContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/organizations/my":
			http.Error(w, "credential expired", http.StatusUnauthorized)
		case "/api/mobile-runners":
			_, _ = w.Write([]byte(`{"runners":[]}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	client := &CredimiClient{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	if _, err := client.MyOrganization(context.Background()); err == nil || !strings.Contains(err.Error(), "credential expired") {
		t.Fatalf("organization lookup error = %v", err)
	}
	if _, err := client.MobileRunnerName(context.Background(), "acme/missing"); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("runner lookup error = %v", err)
	}
}
