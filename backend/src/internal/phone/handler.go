package phone

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"trickle/internal/engine"
	"trickle/internal/httpx"
)

//go:embed web
var webFiles embed.FS

const (
	maxUpload     = engine.MaxTorrentFileSize + 1<<20
	maxMagnetBody = 64 << 10
)

type phoneTorrent struct {
	Name     string       `json:"name"`
	State    engine.State `json:"state"`
	Progress float64      `json:"progress"`
	DownRate int64        `json:"down_rate"`
}

func (s *Server) handler() http.Handler {
	static, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	authed := func(h http.HandlerFunc) http.Handler {
		return httpx.RequireBearer(s.cfg.Logger, "scan the QR code again", s.currentToken, h)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.Handle("GET /phone/v1/torrents", authed(s.list))
	mux.Handle("POST /phone/v1/torrents", authed(s.add))
	return s.limit(secureHeaders(mux))
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.Allow() {
			httpx.WriteError(w, s.cfg.Logger, http.StatusTooManyRequests, "slow down", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	all := s.cfg.Backend.Torrents()
	out := make([]phoneTorrent, 0, len(all))
	for _, t := range all {
		out = append(out, phoneTorrent{Name: t.Name, State: t.State, Progress: t.Progress, DownRate: t.DownRate})
	}
	httpx.WriteData(w, http.StatusOK, out)
}

type magnetRequest struct {
	Magnet string `json:"magnet"`
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	var (
		t   engine.Torrent
		err error
	)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
		t, err = s.addUpload(r)
	} else {
		var req magnetRequest
		if err = httpx.DecodeJSON(w, r, maxMagnetBody, &req); err == nil {
			t, err = s.cfg.Backend.AddMagnet(req.Magnet)
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	httpx.WriteData(w, http.StatusCreated, phoneTorrent{Name: t.Name, State: t.State, Progress: t.Progress})
}

func (s *Server) addUpload(r *http.Request) (engine.Torrent, error) {
	file, _, err := r.FormFile("torrent")
	if err != nil {
		return engine.Torrent{}, errors.Join(engine.ErrInvalid, err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return engine.Torrent{}, errors.Join(engine.ErrInvalid, err)
	}
	return s.cfg.Backend.AddTorrentFile(data)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		httpx.WriteError(w, s.cfg.Logger, http.StatusRequestEntityTooLarge, "file too large", err)
	case errors.Is(err, engine.ErrInvalid):
		httpx.WriteError(w, s.cfg.Logger, http.StatusBadRequest, "not a valid magnet link or .torrent file", err)
	default:
		httpx.WriteError(w, s.cfg.Logger, http.StatusInternalServerError, "could not add", err)
	}
}
