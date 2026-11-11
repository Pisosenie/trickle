package download

import "trickle/internal/bt/peer"

// once nothing is pending it is endgame: a piece someone is fetching may get a second fetcher
func (s *session) assign(has peer.Bitfield) (work, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, w := range s.pending {
		if has.Has(index) {
			delete(s.pending, index)
			s.fetching[index] = w
			s.fetchers[index]++
			return w, true
		}
	}
	if len(s.pending) > 0 {
		return work{}, false
	}
	for index, w := range s.fetching {
		if has.Has(index) && !s.have.Has(index) && s.fetchers[index] < maxFetchers {
			s.fetchers[index]++
			w.duplicate = true
			return w, true
		}
	}
	return work{}, false
}

func (s *session) release(w work, requeue bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fetchers[w.index]--; s.fetchers[w.index] <= 0 {
		delete(s.fetchers, w.index)
		delete(s.fetching, w.index)
	}
	if requeue && !s.have.Has(w.index) {
		original := w
		original.duplicate = false
		s.pending[w.index] = original
	}
}

// wants reports whether the peer has any piece we lack
func (s *session) wants(theirs peer.Bitfield) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	pieces := len(s.tf.Pieces)
	for i := range min(len(theirs), len(s.have)) {
		if extra := theirs[i] &^ s.have[i]; extra != 0 {
			for bit := range 8 {
				if extra&(0x80>>bit) != 0 && i*8+bit < pieces {
					return true
				}
			}
		}
	}
	return false
}
