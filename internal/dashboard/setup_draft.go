package dashboard

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const setupDraftTTL = 24 * time.Hour

// setupDraft is process-local on purpose: it can hold unsaved secrets without
// becoming a second persistent configuration system.
type setupDraft struct {
	ID        string            `json:"id"`
	Step      int               `json:"step"`
	Values    map[string]string `json:"values"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type setupDraftStore struct {
	mu     sync.Mutex
	drafts map[string]setupDraft
}

func newSetupDraftStore() *setupDraftStore { return &setupDraftStore{drafts: map[string]setupDraft{}} }
func (s *setupDraftStore) save(draft setupDraft) (setupDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	if draft.ID == "" {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return setupDraft{}, err
		}
		draft.ID = hex.EncodeToString(raw)
	}
	draft.Values, draft.UpdatedAt = cloneStringMap(draft.Values), time.Now()
	s.drafts[draft.ID] = draft
	return draft, nil
}
func (s *setupDraftStore) get(id string) (setupDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	draft, ok := s.drafts[id]
	if ok {
		draft.Values = cloneStringMap(draft.Values)
	}
	return draft, ok
}
func (s *setupDraftStore) delete(id string) { s.mu.Lock(); delete(s.drafts, id); s.mu.Unlock() }
func (s *setupDraftStore) pruneLocked(now time.Time) {
	for id, draft := range s.drafts {
		if now.Sub(draft.UpdatedAt) > setupDraftTTL {
			delete(s.drafts, id)
		}
	}
}
