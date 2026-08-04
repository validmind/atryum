package managedagents

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// clearSessionsStore is a SessionStore whose Delete fails for one configured
// session ID, letting tests simulate a transient DB error partway through a
// bulk operation like ClearSessions.
type clearSessionsStore struct {
	mu       sync.Mutex
	sessions []SessionRegistration
	failOn   string
	deleted  []string
}

func (s *clearSessionsStore) Upsert(ctx context.Context, r SessionRegistration) error { return nil }
func (s *clearSessionsStore) Get(ctx context.Context, id string) (SessionRegistration, error) {
	return SessionRegistration{SessionID: id}, nil
}
func (s *clearSessionsStore) List(ctx context.Context) ([]SessionRegistration, error) {
	return s.sessions, nil
}
func (s *clearSessionsStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.failOn {
		return errors.New("transient db error")
	}
	s.deleted = append(s.deleted, id)
	return nil
}
func (s *clearSessionsStore) UpdateCursor(ctx context.Context, id, lastEventID string) error {
	return nil
}

// TestClearSessionsStopsWatchersForSessionsDeletedBeforeAFailure reproduces the
// zombie-watcher bug: if Delete fails partway through ClearSessions' session
// list, every watcher whose session row was already removed from the DB must
// still be stopped. Otherwise those watchers keep polling Anthropic and acting
// on tool calls for sessions that no longer exist anywhere else.
func TestClearSessionsStopsWatchersForSessionsDeletedBeforeAFailure(t *testing.T) {
	store := &clearSessionsStore{
		sessions: []SessionRegistration{
			{SessionID: "sess_1"},
			{SessionID: "sess_2"},
			{SessionID: "sess_3"}, // delete fails here
			{SessionID: "sess_4"},
			{SessionID: "sess_5"},
		},
		failOn: "sess_3",
	}

	svc, err := NewService(nil, store, nil, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	var mu sync.Mutex
	stopped := map[string]bool{}
	for _, sess := range store.sessions {
		id := sess.SessionID
		svc.watchers[id] = func() {
			mu.Lock()
			stopped[id] = true
			mu.Unlock()
		}
	}

	cleared, err := svc.ClearSessions(context.Background())
	if err == nil {
		t.Fatal("expected ClearSessions to surface the transient delete error")
	}
	if cleared != 2 {
		t.Errorf("ClearSessions returned cleared=%d, want 2 (sessions fully cleared before the failure)", cleared)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"sess_1", "sess_2"} {
		if !stopped[id] {
			t.Errorf("watcher for %s was not stopped despite its DB row being deleted (zombie watcher)", id)
		}
	}
}
