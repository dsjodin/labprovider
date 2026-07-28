package deploy

import (
	"context"
	"sync"
	"time"
)

// Event is one progress record in a run's stream.
type Event struct {
	Seq     int       `json:"seq"`
	Time    time.Time `json:"time"`
	Type    string    `json:"type"` // step-start | log | step-done | step-failed | deploy-done | deploy-failed
	Service string    `json:"service,omitempty"`
	Line    string    `json:"line,omitempty"`
}

// Run is one deploy/remove execution. Events are buffered for replay so an
// SSE subscriber that connects late still sees the full log.
type Run struct {
	ID       int      `json:"id"`
	Services []string `json:"services"`
	Skipped  []string `json:"skipped,omitempty"` // deps left out because already deployed
	Remove   bool     `json:"remove"`

	mu     sync.Mutex
	events []Event
	subs   []chan Event
	done   bool
	result string
	cancel context.CancelFunc // set once execute starts; cancels the run's context
}

func (r *Run) setCancel(cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel = cancel
}

func (r *Run) cancelRun() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func newRun(id int, services []string, remove bool) *Run {
	return &Run{ID: id, Services: services, Remove: remove}
}

func (r *Run) emit(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev.Seq = len(r.events)
	ev.Time = time.Now()
	ev.Line = stripANSI(ev.Line)
	r.events = append(r.events, ev)
	live := r.subs[:0]
	for _, ch := range r.subs {
		select {
		case ch <- ev:
			live = append(live, ch)
		default:
			// A stalled subscriber must not block the deploy, and silently
			// dropping lines from the middle of the log is worse than a gap the
			// reader can see. Close it instead: the browser's EventSource
			// reconnects and Subscribe replays every event from the start.
			close(ch)
		}
	}
	r.subs = live
}

func (r *Run) finish(result string) {
	r.emit(Event{Type: result})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	r.result = result
	for _, ch := range r.subs {
		close(ch)
	}
	r.subs = nil
}

// Done reports whether the run has finished.
func (r *Run) Done() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

// Result returns "" while running, else deploy-done or deploy-failed.
func (r *Run) Result() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

// Subscribe returns a replay of past events and a channel of future ones.
// The channel is nil when the run has already finished.
func (r *Run) Subscribe() ([]Event, <-chan Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replay := make([]Event, len(r.events))
	copy(replay, r.events)
	if r.done {
		return replay, nil
	}
	ch := make(chan Event, 256)
	r.subs = append(r.subs, ch)
	return replay, ch
}

// Unsubscribe drops a channel Subscribe handed out. Without it every closed
// tab, every navigation away from /deploy, and every EventSource reconnect -
// which emit's close-on-stall makes routine - left a channel and up to 256
// buffered events alive for the rest of the run, plus a select arm on every
// emit. Bounded, but on a long Harbor deploy with a few reconnects that is
// thousands of retained events for nobody.
//
// Closing here is safe against emit and finish, which both hold r.mu and both
// drop the channel from r.subs when they close it.
func (r *Run) Unsubscribe(ch <-chan Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.subs {
		if (<-chan Event)(c) == ch {
			r.subs = append(r.subs[:i], r.subs[i+1:]...)
			close(c)
			return
		}
	}
}
