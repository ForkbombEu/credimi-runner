package dashboard

import "testing"

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
