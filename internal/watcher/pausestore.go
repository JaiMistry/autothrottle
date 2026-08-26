package watcher

import (
	"github.com/JaiMistry/autothrottle/internal/clients"
	"github.com/JaiMistry/autothrottle/internal/db"
)

// pauseStore binds the database's pause table to one media server, so a
// watcher can persist its clocks without knowing which server it is.
type pauseStore struct {
	db            *db.DB
	mediaServerID int64
}

func (p pauseStore) Load() (map[string]clients.PausedSession, error) {
	saved, err := p.db.LoadPausedSessions(p.mediaServerID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]clients.PausedSession, len(saved))
	for key, state := range saved {
		out[key] = clients.PausedSession{Since: state.Since, Position: state.Position}
	}
	return out, nil
}

func (p pauseStore) Save(sessionKey string, state clients.PausedSession) error {
	return p.db.SavePausedSession(p.mediaServerID, sessionKey,
		db.PausedSession{Since: state.Since, Position: state.Position})
}

func (p pauseStore) Delete(sessionKey string) error {
	return p.db.DeletePausedSession(p.mediaServerID, sessionKey)
}

func (p pauseStore) Retain(present map[string]bool) error {
	return p.db.RetainPausedSessions(p.mediaServerID, present)
}
