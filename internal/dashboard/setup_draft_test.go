package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSetupDraftStoreKeepsValuesOnlyInProcess(t *testing.T) {
	store := newSetupDraftStore()
	saved, err := store.save(setupDraft{Step: 2, Values: map[string]string{"CREDIMI_USER_API_KEY": "secret", "CREDIMI_RUNNER_NAME": "runner"}})
	if err != nil || saved.ID == "" {
		t.Fatalf("save = %#v, %v", saved, err)
	}
	loaded, ok := store.get(saved.ID)
	if !ok || loaded.Values["CREDIMI_RUNNER_NAME"] != "runner" {
		t.Fatalf("get = %#v, %t", loaded, ok)
	}
	loaded.Values["CREDIMI_RUNNER_NAME"] = "mutated"
	again, _ := store.get(saved.ID)
	if again.Values["CREDIMI_RUNNER_NAME"] != "runner" {
		t.Fatalf("draft escaped store copy: %#v", again)
	}
	store.delete(saved.ID)
	if _, ok := store.get(saved.ID); ok {
		t.Fatal("deleted draft remained available")
	}
}

func TestSetupDraftStoreIDsAndExpiry(t *testing.T) {
	store := newSetupDraftStore()
	first, err := store.save(setupDraft{ID: "not-an-opaque-id", Values: map[string]string{"one": "1"}})
	if err != nil || len(first.ID) != 48 || strings.Trim(first.ID, "0123456789abcdef") != "" {
		t.Fatalf("invalid ID was not replaced: %#v, %v", first, err)
	}
	second, err := store.save(setupDraft{Values: map[string]string{"two": "2"}})
	if err != nil || second.ID == first.ID {
		t.Fatalf("draft IDs are not isolated: %#v %#v", first, second)
	}
	store.mu.Lock()
	store.drafts[first.ID] = setupDraft{ID: first.ID, UpdatedAt: time.Now().Add(-setupDraftTTL - time.Minute), Values: map[string]string{"old": "value"}}
	store.mu.Unlock()
	if _, ok := store.get(first.ID); ok {
		t.Fatal("expired draft survived pruning")
	}
	if loaded, ok := store.get(second.ID); !ok || loaded.Values["two"] != "2" {
		t.Fatalf("unrelated draft was pruned: %#v %t", loaded, ok)
	}
}

func TestSetupDraftHTTPHandlers(t *testing.T) {
	server := newTestServer(t)
	server.setupDrafts = newSetupDraftStore()
	bad := httptest.NewRecorder()
	server.saveSetupDraft(bad, httptest.NewRequest(http.MethodPost, "/setup/draft", strings.NewReader("{")))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid draft status = %d", bad.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/setup/draft", bytes.NewBufferString(`{"step":2,"values":{"CREDIMI_USER_API_KEY":"secret"}}`))
	got := httptest.NewRecorder()
	server.saveSetupDraft(got, req)
	if got.Code != http.StatusOK || got.Header().Get("Cache-Control") != "no-store" || got.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("save draft response = %d, headers %#v", got.Code, got.Header())
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &saved); err != nil || !validSetupDraftID(saved.ID) {
		t.Fatalf("saved draft ID = %#v, %v", saved, err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/setup/draft/"+saved.ID, nil)
	getReq.SetPathValue("id", saved.ID)
	get := httptest.NewRecorder()
	server.getSetupDraft(get, getReq)
	if get.Code != http.StatusOK || get.Header().Get("Cache-Control") != "no-store" || !strings.Contains(get.Body.String(), "secret") {
		t.Fatalf("get draft response = %d, headers %#v, body %q", get.Code, get.Header(), get.Body.String())
	}
	deleteReq := httptest.NewRequest(http.MethodDelete, "/setup/draft/"+saved.ID, nil)
	deleteReq.SetPathValue("id", saved.ID)
	deleted := httptest.NewRecorder()
	server.deleteSetupDraft(deleted, deleteReq)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete draft status = %d", deleted.Code)
	}
	missingReq := httptest.NewRequest(http.MethodGet, "/setup/draft/"+saved.ID, nil)
	missingReq.SetPathValue("id", saved.ID)
	missing := httptest.NewRecorder()
	server.getSetupDraft(missing, missingReq)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted draft status = %d", missing.Code)
	}
}
