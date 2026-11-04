package workspace

import (
	"container/list"
	"crypto/rand"
	"net/http"
	"sync"
	"time"
)

const (
	sessionCookie    = "query_workspace_session"
	sessionIdleTTL   = time.Hour
	sessionMemoryCap = 64 << 20
)

// runSnapshot is an immutable record of one finished run; saving uses it instead of form data.
type runSnapshot struct {
	ID            string
	Investigation Investigation
	Error         string
	Input         string
	Parameters    string
	Saved         *SavedQuery
}

// modeState is what one composer tab remembers: the editor text and its last run.
type modeState struct {
	Draft      string
	Parameters string
	Run        *runSnapshot
}

type browserSession struct {
	id         string
	lastUsed   time.Time
	activeMode InputMode
	modes      map[InputMode]modeState
	size       int
	element    *list.Element
}

// sessionStore keeps per-browser drafts and last runs in memory. Sessions expire after an idle
// period; when the memory budget is exceeded, expired sessions go first, then the least recently used.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*browserSession
	recency  *list.List
	used     int
	budget   int
	ttl      time.Duration
	now      func() time.Time
}

func newSessionStore(budget int, ttl time.Duration, now func() time.Time) *sessionStore {
	return &sessionStore{sessions: make(map[string]*browserSession), recency: list.New(), budget: budget, ttl: ttl, now: now}
}

// sessionView is a copy of a session's state, safe to use outside the store lock.
type sessionView struct {
	ID         string
	ActiveMode InputMode
	Modes      map[InputMode]modeState
	// Ended is set when the browser presented a session the server no longer holds.
	Ended bool
}

// load returns the request's session, creating one (and setting its cookie) when needed.
func (s *sessionStore) load(w http.ResponseWriter, r *http.Request) sessionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	cookie, err := r.Cookie(sessionCookie)
	if err == nil {
		if session, ok := s.sessions[cookie.Value]; ok {
			s.touch(session)
			return session.view()
		}
	}
	session := &browserSession{id: rand.Text(), activeMode: ModeAsk, modes: make(map[InputMode]modeState)}
	session.lastUsed = s.now()
	session.element = s.recency.PushFront(session)
	s.sessions[session.id] = session
	session.size = session.measure()
	s.used += session.size
	s.evict(session)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: session.id, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	view := session.view()
	view.Ended = err == nil
	return view
}

// update applies change to the session if it still exists. It reports false when the session ended
// between load and update, in which case nothing is stored.
func (s *sessionStore) update(id string, change func(*browserSession)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return false
	}
	change(session)
	s.touch(session)
	s.used -= session.size
	session.size = session.measure()
	s.used += session.size
	s.evict(session)
	return true
}

func (s *sessionStore) touch(session *browserSession) {
	session.lastUsed = s.now()
	s.recency.MoveToFront(session.element)
}

func (s *sessionStore) expire() {
	for element := s.recency.Back(); element != nil; {
		session := element.Value.(*browserSession)
		if s.now().Sub(session.lastUsed) < s.ttl {
			return
		}
		previous := element.Prev()
		s.remove(session)
		element = previous
	}
}

// evict frees memory by dropping expired, then least recently used sessions, never the current one.
func (s *sessionStore) evict(current *browserSession) {
	s.expire()
	for element := s.recency.Back(); s.used > s.budget && element != nil; {
		session := element.Value.(*browserSession)
		previous := element.Prev()
		if session != current {
			s.remove(session)
		}
		element = previous
	}
}

func (s *sessionStore) remove(session *browserSession) {
	s.recency.Remove(session.element)
	delete(s.sessions, session.id)
	s.used -= session.size
}

func (b *browserSession) view() sessionView {
	modes := make(map[InputMode]modeState, len(b.modes))
	for mode, state := range b.modes {
		modes[mode] = state
	}
	return sessionView{ID: b.id, ActiveMode: b.activeMode, Modes: modes}
}

// measure approximates retained bytes; it only needs to be proportionate for the memory budget.
func (b *browserSession) measure() int {
	size := 256
	for _, state := range b.modes {
		size += len(state.Draft) + len(state.Parameters)
		if run := state.Run; run != nil {
			size += 512 + len(run.Input) + len(run.Parameters) + len(run.Error) + investigationSize(run.Investigation)
		}
	}
	return size
}

func investigationSize(investigation Investigation) int {
	size := len(investigation.Question) + len(investigation.Query.Cypher) + 128*len(investigation.Query.Parameters)
	for _, column := range investigation.Result.Columns {
		size += len(column) + 16
	}
	for _, row := range investigation.Result.Rows {
		for _, cell := range row {
			size += len(cell) + 16
		}
	}
	graph := investigation.Result.Graph
	for _, node := range graph.Nodes {
		size += len(node.ID) + 64
		for _, label := range node.Labels {
			size += len(label) + 16
		}
		for key, value := range node.Properties {
			size += len(key) + len(value) + 32
		}
	}
	for _, relationship := range graph.Relationships {
		size += len(relationship.ID) + len(relationship.Type) + len(relationship.StartID) + len(relationship.EndID) + 64
		for key, value := range relationship.Properties {
			size += len(key) + len(value) + 32
		}
	}
	for _, ids := range graph.RowNodes {
		for _, id := range ids {
			size += len(id) + 16
		}
	}
	if investigation.Query.Diagnostics != nil {
		size += 4096
	}
	return size
}
