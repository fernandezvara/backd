package realtime

import (
	"context"
	"encoding/json"
	"time"
)

// EventConfigChanged tells a stream with a configuration subscription that what its caller
// reads of the app configuration changed (design/app-config.md §8).
const EventConfigChanged = "config_changed"

// ConfigSub is a stream's subscription to the app configuration. It is not tied to a
// collection: a change of the realm's configuration makes the stream ask Check what, of
// everything its caller may read, changed.
type ConfigSub struct {
	ID string
	// Check reads the configuration as the stream's caller sees it now and returns what
	// differs from the last call; nil when nothing the caller reads changed.
	Check func(ctx context.Context) (*ConfigNotice, error)
}

// ConfigNotice is the data of a `config_changed` event.
type ConfigNotice struct {
	Version string   `json:"version"`
	Paths   []string `json:"paths"` // the keys that changed; empty: reload everything
}

// configRefresh runs after a change of the configuration, or of the caller's access: it asks
// the subscription what changed and queues the event. Changes that arrive while a check runs
// are folded into one more check.
func (s *Stream) configRefresh() {
	if s.config == nil {
		return
	}
	s.mu.Lock()
	if s.closed || s.ending {
		s.mu.Unlock()
		return
	}
	if s.checking {
		s.recheck = true
		s.mu.Unlock()
		return
	}
	s.checking = true
	s.mu.Unlock()
	go func() {
		for {
			// Every subscriber of the realm is told at once: spread the reads, as for access.
			time.Sleep(s.hub.reloadDelay())
			release := s.hub.acquireReload()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			notice, err := s.config.Check(ctx)
			cancel()
			release()
			if err != nil {
				// Nothing may be shown on a guess: tell the client to read it all again.
				notice = &ConfigNotice{}
			}
			s.sendConfig(notice)
			s.mu.Lock()
			if !s.recheck || s.closed || s.ending {
				s.checking, s.recheck = false, false
				s.mu.Unlock()
				return
			}
			s.recheck = false
			s.mu.Unlock()
		}
	}()
}

func (s *Stream) sendConfig(n *ConfigNotice) {
	if n == nil {
		return
	}
	paths := n.Paths
	if paths == nil {
		paths = []string{}
	}
	data, _ := json.Marshal(struct {
		ID      string   `json:"id"`
		Version string   `json:"version"`
		Paths   []string `json:"paths"`
	}{s.config.ID, n.Version, paths})
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	delta := s.enqueueLocked(Frame{Event: EventConfigChanged, Data: data})
	s.mu.Unlock()
	s.hub.charge(delta)
	s.wake()
}
