package api

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"

	"trickle/internal/disk"
	"trickle/internal/engine"
	"trickle/internal/events"
	"trickle/internal/httpx"
	"trickle/internal/phone"
	"trickle/internal/service"
	"trickle/internal/settings"
)

type Backend interface {
	Status() service.Status
	Torrents() []engine.Torrent
	AddMagnet(uri string) (engine.Torrent, error)
	AddTorrentFile(data []byte) (engine.Torrent, error)
	AddTorrentPath(path string) (engine.Torrent, error)
	Pause(id string) error
	Resume(id string) error
	Remove(id string, deleteFiles bool) error
	Settings() settings.Settings
	UpdateSettings(next settings.Settings) error
	SetGameRunning(running bool)
	Locations() []disk.Location
}

type Phone interface {
	Info() phone.Info
	RotateToken() error
}

type Deps struct {
	Logger  *slog.Logger
	Backend Backend
	Phone   Phone
	Hub     *events.Hub
	Token   string
}

const maxJSONBody = 1 << 20

type handler struct {
	log     *slog.Logger
	backend Backend
	phone   Phone
	hub     *events.Hub
}

type overview struct {
	Status   service.Status   `json:"status"`
	Torrents []engine.Torrent `json:"torrents"`
}

func NewHandler(deps Deps) http.Handler {
	h := handler{log: deps.Logger, backend: deps.Backend, phone: deps.Phone, hub: deps.Hub}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/overview", h.overview)
	mux.HandleFunc("POST /api/v1/torrents", h.addTorrent)
	mux.HandleFunc("POST /api/v1/torrents/{id}/pause", h.pause)
	mux.HandleFunc("POST /api/v1/torrents/{id}/resume", h.resume)
	mux.HandleFunc("DELETE /api/v1/torrents/{id}", h.remove)
	mux.HandleFunc("GET /api/v1/settings", h.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", h.putSettings)
	mux.HandleFunc("PUT /api/v1/game", h.putGame)
	mux.HandleFunc("GET /api/v1/locations", h.locations)
	mux.HandleFunc("GET /api/v1/phone", h.phoneInfo)
	mux.HandleFunc("POST /api/v1/phone/token", h.rotateToken)
	mux.HandleFunc("GET /api/v1/events", h.events)

	token := func() string { return deps.Token }
	return loopbackOnly(httpx.RequireBearer(deps.Logger, "unauthorized", token, mux))
}

// a web page can't send Authorization cross-origin or pass the Host check
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpx.IsLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h handler) overview(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteData(w, http.StatusOK, overview{Status: h.backend.Status(), Torrents: h.backend.Torrents()})
}

type addRequest struct {
	Magnet        string `json:"magnet,omitempty"`
	TorrentPath   string `json:"torrent_path,omitempty"`
	TorrentBase64 string `json:"torrent_base64,omitempty"`
}

func (h handler) addTorrent(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := httpx.DecodeJSON(w, r, maxJSONBody, &req); err != nil {
		httpx.WriteError(w, h.log, http.StatusBadRequest, "malformed request body", err)
		return
	}
	t, err := h.add(req)
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteData(w, http.StatusCreated, t)
}

func (h handler) add(req addRequest) (engine.Torrent, error) {
	switch {
	case req.Magnet != "" && req.TorrentPath == "" && req.TorrentBase64 == "":
		return h.backend.AddMagnet(req.Magnet)
	case req.TorrentPath != "" && req.Magnet == "" && req.TorrentBase64 == "":
		return h.backend.AddTorrentPath(req.TorrentPath)
	case req.TorrentBase64 != "" && req.Magnet == "" && req.TorrentPath == "":
		data, err := base64.StdEncoding.DecodeString(req.TorrentBase64)
		if err != nil {
			return engine.Torrent{}, errors.Join(engine.ErrInvalid, err)
		}
		return h.backend.AddTorrentFile(data)
	default:
		return engine.Torrent{}, errors.Join(engine.ErrInvalid,
			errors.New("send exactly one of magnet, torrent_path, torrent_base64"))
	}
}

func (h handler) pause(w http.ResponseWriter, r *http.Request) {
	h.done(w, h.backend.Pause(r.PathValue("id")))
}

func (h handler) resume(w http.ResponseWriter, r *http.Request) {
	h.done(w, h.backend.Resume(r.PathValue("id")))
}

func (h handler) remove(w http.ResponseWriter, r *http.Request) {
	deleteFiles := r.URL.Query().Get("delete_files") == "true"
	h.done(w, h.backend.Remove(r.PathValue("id"), deleteFiles))
}

func (h handler) getSettings(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteData(w, http.StatusOK, h.backend.Settings())
}

func (h handler) putSettings(w http.ResponseWriter, r *http.Request) {
	var next settings.Settings
	if err := httpx.DecodeJSON(w, r, maxJSONBody, &next); err != nil {
		httpx.WriteError(w, h.log, http.StatusBadRequest, "malformed settings", err)
		return
	}
	if err := h.backend.UpdateSettings(next); err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, h.backend.Settings())
}

type gameRequest struct {
	Running bool `json:"running"`
}

func (h handler) putGame(w http.ResponseWriter, r *http.Request) {
	var req gameRequest
	if err := httpx.DecodeJSON(w, r, maxJSONBody, &req); err != nil {
		httpx.WriteError(w, h.log, http.StatusBadRequest, "malformed request body", err)
		return
	}
	h.backend.SetGameRunning(req.Running)
	httpx.WriteData(w, http.StatusOK, nil)
}

func (h handler) locations(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteData(w, http.StatusOK, h.backend.Locations())
}

func (h handler) phoneInfo(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteData(w, http.StatusOK, h.phone.Info())
}

func (h handler) rotateToken(w http.ResponseWriter, _ *http.Request) {
	if err := h.phone.RotateToken(); err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, h.phone.Info())
}

func (h handler) done(w http.ResponseWriter, err error) {
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, nil)
}

func (h handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrNotFound):
		httpx.WriteError(w, h.log, http.StatusNotFound, "torrent not found", err)
	case errors.Is(err, engine.ErrInvalid):
		httpx.WriteError(w, h.log, http.StatusBadRequest, err.Error(), err)
	default:
		httpx.WriteError(w, h.log, http.StatusInternalServerError, "internal error", err)
	}
}
