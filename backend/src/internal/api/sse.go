package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (h handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	stream, cancel := h.hub.Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-stream:
			if !open {
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				h.log.Error("cannot encode event", "type", ev.Type, "err", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
