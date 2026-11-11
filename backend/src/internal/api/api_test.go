package api

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"trickle/internal/disk"
	"trickle/internal/engine"
	"trickle/internal/events"
	"trickle/internal/phone"
	"trickle/internal/service"
	"trickle/internal/settings"
)

type fakeBackend struct {
	hub      *events.Hub
	settings settings.Settings
	game     bool
	removed  string
	deleted  bool
}

func (f *fakeBackend) Status() service.Status     { return service.Status{FreeBytes: 1} }
func (f *fakeBackend) Torrents() []engine.Torrent { return []engine.Torrent{{ID: "a", Name: "debian"}} }
func (f *fakeBackend) AddMagnet(uri string) (engine.Torrent, error) {
	if !strings.HasPrefix(uri, "magnet:") {
		return engine.Torrent{}, engine.ErrInvalid
	}
	return engine.Torrent{ID: "m", Name: "from magnet"}, nil
}
func (f *fakeBackend) AddTorrentFile(data []byte) (engine.Torrent, error) {
	return engine.Torrent{ID: "f", Name: string(data)}, nil
}
func (f *fakeBackend) AddTorrentPath(string) (engine.Torrent, error) {
	return engine.Torrent{ID: "p"}, nil
}
func (f *fakeBackend) Pause(id string) error {
	if id != "a" {
		return engine.ErrNotFound
	}
	return nil
}
func (f *fakeBackend) Resume(string) error { return nil }
func (f *fakeBackend) Remove(id string, deleteFiles bool) error {
	f.removed, f.deleted = id, deleteFiles
	return nil
}
func (f *fakeBackend) Settings() settings.Settings { return f.settings }
func (f *fakeBackend) UpdateSettings(next settings.Settings) error {
	if err := next.Validate(); err != nil {
		return engine.ErrInvalid
	}
	f.settings = next
	return nil
}
func (f *fakeBackend) SetGameRunning(running bool) { f.game = running }
func (f *fakeBackend) Locations() []disk.Location  { return nil }

type fakePhone struct{}

func (fakePhone) Info() phone.Info   { return phone.Info{QRPNGBase64: "x"} }
func (fakePhone) RotateToken() error { return nil }

const testToken = "test-token"

func authed(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	return req
}

func newTestServer(t *testing.T) (*httptest.Server, *fakeBackend) {
	t.Helper()
	backend := &fakeBackend{hub: events.NewHub(), settings: settings.Defaults("/home/deck/Downloads")}
	srv := httptest.NewServer(NewHandler(Deps{
		Logger:  slog.New(slog.DiscardHandler),
		Backend: backend,
		Phone:   fakePhone{},
		Hub:     backend.hub,
		Token:   testToken,
	}))
	t.Cleanup(srv.Close)
	return srv, backend
}

func do(t *testing.T, srv *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := authed(t, method, srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestRoutes(t *testing.T) {
	srv, backend := newTestServer(t)
	cases := []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/api/v1/overview", "", 200},
		{"POST", "/api/v1/torrents", `{"magnet":"magnet:?xt=urn:btih:abc"}`, 201},
		{"POST", "/api/v1/torrents", `{"magnet":"http://nope"}`, 400},
		{"POST", "/api/v1/torrents", `{"torrent_base64":"aGVsbG8="}`, 201},
		{"POST", "/api/v1/torrents", `{"torrent_base64":"%%%"}`, 400},
		{"POST", "/api/v1/torrents", `{"magnet":"magnet:?x","torrent_path":"/a.torrent"}`, 400},
		{"POST", "/api/v1/torrents", `{"unknown":1}`, 400},
		{"POST", "/api/v1/torrents/a/pause", "", 200},
		{"POST", "/api/v1/torrents/zzz/pause", "", 404},
		{"POST", "/api/v1/torrents/a/resume", "", 200},
		{"PUT", "/api/v1/settings", `{"download_dir":"relative"}`, 400},
		{"PUT", "/api/v1/game", `{"running":true}`, 200},
		{"GET", "/api/v1/phone", "", 200},
		{"GET", "/api/v1/nope", "", 404},
	}
	for _, tc := range cases {
		status, body := do(t, srv, tc.method, tc.path, tc.body)
		if status != tc.want {
			t.Errorf("%s %s %s: status %d, want %d (%v)", tc.method, tc.path, tc.body, status, tc.want, body)
		}
	}
	if !backend.game {
		t.Error("game flag not passed to the backend")
	}

	status, _ := do(t, srv, "DELETE", "/api/v1/torrents/a?delete_files=true", "")
	if status != 200 || backend.removed != "a" || !backend.deleted {
		t.Errorf("delete: status %d, removed %q, deleted %v", status, backend.removed, backend.deleted)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)
	next := settings.Defaults("/run/media/sd/Downloads")
	next.GameMode = settings.GameLimit
	body, _ := json.Marshal(next)
	status, out := do(t, srv, "PUT", "/api/v1/settings", string(body))
	if status != 200 {
		t.Fatalf("status %d: %v", status, out)
	}
	data := out["data"].(map[string]any)
	if data["download_dir"] != "/run/media/sd/Downloads" || data["game_mode"] != "limit" {
		t.Fatalf("settings not stored: %v", data)
	}
}

func TestGuard(t *testing.T) {
	srv, _ := newTestServer(t)
	send := func(req *http.Request) int {
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	foreign := authed(t, "GET", srv.URL+"/api/v1/overview", nil)
	foreign.Host = "evil.example:47610"
	if code := send(foreign); code != http.StatusForbidden {
		t.Errorf("foreign Host: status %d", code)
	}

	noToken, _ := http.NewRequest("POST", srv.URL+"/api/v1/phone/token", nil)
	if code := send(noToken); code != http.StatusUnauthorized {
		t.Errorf("bodyless POST without token: status %d", code)
	}

	wrong, _ := http.NewRequest("GET", srv.URL+"/api/v1/overview", nil)
	wrong.Header.Set("Authorization", "Bearer nope")
	if code := send(wrong); code != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d", code)
	}
}

func TestEventsStream(t *testing.T) {
	srv, backend := newTestServer(t)
	res, err := srv.Client().Do(authed(t, "GET", srv.URL+"/api/v1/events", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		backend.hub.Publish(events.Event{Type: events.TorrentCompleted, Data: map[string]string{"name": "debian"}})
	}()

	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				lines <- line
				return
			}
		}
		close(lines)
	}()
	select {
	case line := <-lines:
		if !strings.Contains(line, `"type":"torrent_completed"`) || !strings.Contains(line, "debian") {
			t.Fatalf("unexpected event line %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event received")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 0))
}
