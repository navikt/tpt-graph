package workspace

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type sessionClock struct{ now time.Time }

func (c *sessionClock) Now() time.Time { return c.now }

func loadSession(store *sessionStore, cookie string) sessionView {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	return store.load(httptest.NewRecorder(), request)
}

func TestSessionStoreExpiresIdleSessionsAndReportsTheEnd(t *testing.T) {
	clock := &sessionClock{now: time.Unix(0, 0)}
	store := newSessionStore(1<<20, time.Hour, clock.Now)
	session := loadSession(store, "")
	if session.Ended {
		t.Fatal("a first visit is not an ended session")
	}
	clock.now = clock.now.Add(59 * time.Minute)
	if again := loadSession(store, session.ID); again.ID != session.ID || again.Ended {
		t.Fatal("an active session was not kept")
	}
	clock.now = clock.now.Add(time.Hour)
	if expired := loadSession(store, session.ID); expired.ID == session.ID || !expired.Ended {
		t.Fatal("an idle session was not expired and reported")
	}
}

func TestSessionStoreEvictsLeastRecentlyUsedButNeverTheCurrentSession(t *testing.T) {
	clock := &sessionClock{now: time.Unix(0, 0)}
	store := newSessionStore(4000, time.Hour, clock.Now)
	draft := func(id string, size int) {
		store.update(id, func(s *browserSession) { s.modes[ModeAsk] = modeState{Draft: strings.Repeat("x", size)} })
	}
	oldest, recent, current := loadSession(store, ""), loadSession(store, ""), loadSession(store, "")
	draft(oldest.ID, 1000)
	draft(recent.ID, 1000)
	loadSession(store, oldest.ID)
	draft(current.ID, 1500)
	if loadSession(store, recent.ID).Ended == false {
		t.Fatal("the least recently used session was not evicted")
	}
	if loadSession(store, oldest.ID).Ended {
		t.Fatal("a recently used session was evicted before the least recently used one")
	}
	draft(current.ID, 10000)
	if view := loadSession(store, current.ID); view.Ended || len(view.Modes[ModeAsk].Draft) != 10000 {
		t.Fatal("the session being updated was evicted")
	}
}

func TestSessionStoreCountsNewSessionsAgainstTheBudget(t *testing.T) {
	store := newSessionStore(1000, time.Hour, (&sessionClock{now: time.Unix(0, 0)}).Now)
	first := loadSession(store, "")
	for range 10 {
		loadSession(store, "")
	}
	if !loadSession(store, first.ID).Ended {
		t.Fatal("empty sessions bypassed the memory budget")
	}
	if store.used > store.budget {
		t.Fatalf("used %d bytes, budget %d", store.used, store.budget)
	}
}
