package utp

import "time"

const (
	targetDelay   = 100_000
	gainPerRTT    = 3000
	initialWindow = 10 * maxPayload
	maxWindow     = 1 << 20
	baseHistory   = 2 * time.Minute
)

// ledbat grows the window while the queueing delay we add stays under the target and shrinks it above,
// so uTP backs off as soon as anything else (a game, a call) starts filling the line (BEP 29)
type ledbat struct {
	window int
	base   baseDelay
}

func newLedbat() ledbat {
	return ledbat{window: initialWindow}
}

// delay is the peer's view of how long our packets travelled, offset by the clock difference;
// the minimum seen recently cancels the offset and leaves the queueing part
func (l *ledbat) onAck(delay uint32, bytes int, now time.Time) {
	if delay == 0 {
		return
	}
	l.base.add(delay, now)
	queued := int64(delay - l.base.min())
	offTarget := float64(targetDelay-queued) / targetDelay
	gain := gainPerRTT * offTarget * float64(bytes) / float64(max(l.window, bytes))
	l.window = min(max(l.window+int(gain), maxPayload), maxWindow)
}

func (l *ledbat) onLoss() {
	l.window = max(l.window/2, maxPayload)
}

func (l *ledbat) onTimeout() {
	l.window = maxPayload
}

// two overlapping minutes of minimums, so an old low sample ages out within the history window
type baseDelay struct {
	cur, next uint32
	set       bool
	rotated   time.Time
}

func (b *baseDelay) add(d uint32, now time.Time) {
	if !b.set {
		b.cur, b.next, b.set, b.rotated = d, d, true, now
		return
	}
	if int32(d-b.cur) < 0 {
		b.cur = d
	}
	if int32(d-b.next) < 0 {
		b.next = d
	}
	if now.Sub(b.rotated) >= baseHistory/2 {
		b.cur, b.next, b.rotated = b.next, d, now
	}
}

func (b *baseDelay) min() uint32 {
	return b.cur
}
