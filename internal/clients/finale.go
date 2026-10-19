package clients

import (
	"log/slog"
	"sync"
	"time"
)

// episodeNumber is an episode's place in its show. Season 0 is specials.
type episodeNumber struct{ season, episode int }

// isFinale reports whether nothing in a show comes after current: no
// later episode in its season, and no later season. Specials aside, as
// autoplay runs through the seasons. An unnumbered episode can't be
// placed, and counts as having a next.
func isFinale(current episodeNumber, show []episodeNumber) bool {
	if current.season <= 0 || current.episode <= 0 {
		return false
	}
	for _, e := range show {
		if e.season > current.season || (e.season == current.season && e.episode > current.episode) {
			return false
		}
	}
	return true
}

// finaleCacheFor is how long an answer stands: a show can gain an
// episode, but not usually while one of its others is playing.
const finaleCacheFor = time.Hour

// finaleCache asks once per episode, when it is first seen, whether it is
// its show's last, rather than on every report of it.
type finaleCache struct {
	mu      sync.Mutex
	answers map[string]finaleAnswer
}

type finaleAnswer struct {
	finale bool
	at     time.Time
}

// lookup answers from the cache, or asks. A failed ask counts as not a
// finale, holding the throttle as for any episode, and is asked again
// next time.
func (c *finaleCache) lookup(itemID string, ask func() (bool, error)) bool {
	now := time.Now()
	c.mu.Lock()
	a, ok := c.answers[itemID]
	c.mu.Unlock()
	if ok && now.Sub(a.at) < finaleCacheFor {
		return a.finale
	}
	finale, err := ask()
	if err != nil {
		slog.Debug("checking for a next episode", "item", itemID, "error", Redact(err))
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.answers == nil {
		c.answers = make(map[string]finaleAnswer)
	}
	for id, old := range c.answers {
		if now.Sub(old.at) >= finaleCacheFor {
			delete(c.answers, id)
		}
	}
	c.answers[itemID] = finaleAnswer{finale: finale, at: now}
	return finale
}
