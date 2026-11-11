package phone

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"trickle/internal/engine"
)

type fakeBackend struct{ added []string }

func (f *fakeBackend) Torrents() []engine.Torrent {
	return []engine.Torrent{{Name: "debian.iso", State: engine.StateDownloading, Progress: 0.5, SaveDir: "/home/deck"}}
}

func (f *fakeBackend) AddMagnet(uri string) (engine.Torrent, error) {
	if !strings.HasPrefix(uri, "magnet:") {
		return engine.Torrent{}, engine.ErrInvalid
	}
	f.added = append(f.added, uri)
	return engine.Torrent{Name: "magnet"}, nil
}

func (f *fakeBackend) AddTorrentFile(data []byte) (engine.Torrent, error) {
	f.added = append(f.added, string(data))
	return engine.Torrent{Name: "file"}, nil
}

func newTestPhone(t *testing.T) (*Server, *httptest.Server, *fakeBackend) {
	t.Helper()
	backend := &fakeBackend{}
	s, err := New(Config{
		Logger:    slog.New(slog.DiscardHandler),
		Backend:   backend,
		Port:      0,
		TokenPath: filepath.Join(t.TempDir(), "token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return s, srv, backend
}

func request(t *testing.T, srv *httptest.Server, method, token, contentType string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+"/phone/v1/torrents", body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestAuth(t *testing.T) {
	s, srv, _ := newTestPhone(t)
	if res := request(t, srv, "GET", "", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d", res.StatusCode)
	}
	if res := request(t, srv, "GET", "wrong", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", res.StatusCode)
	}
	res := request(t, srv, "GET", s.currentToken(), "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("valid token: %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), "/home/deck") {
		t.Error("phone list must not expose local paths")
	}
}

func TestAddMagnetAndFile(t *testing.T) {
	s, srv, backend := newTestPhone(t)
	token := s.currentToken()

	magnet, _ := json.Marshal(magnetRequest{Magnet: "magnet:?xt=urn:btih:abc"})
	if res := request(t, srv, "POST", token, "application/json", bytes.NewReader(magnet)); res.StatusCode != http.StatusCreated {
		t.Errorf("magnet: %d", res.StatusCode)
	}
	bad, _ := json.Marshal(magnetRequest{Magnet: "javascript:alert(1)"})
	if res := request(t, srv, "POST", token, "application/json", bytes.NewReader(bad)); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad magnet: %d", res.StatusCode)
	}

	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	part, _ := form.CreateFormFile("torrent", "debian.torrent")
	part.Write([]byte("d4:infoe"))
	form.Close()
	if res := request(t, srv, "POST", token, form.FormDataContentType(), &buf); res.StatusCode != http.StatusCreated {
		t.Errorf("upload: %d", res.StatusCode)
	}
	if len(backend.added) != 2 || backend.added[1] != "d4:infoe" {
		t.Fatalf("backend got %v", backend.added)
	}
}

func TestStaticPage(t *testing.T) {
	_, srv, _ := newTestPhone(t)
	res, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(body), "Send to Deck") {
		t.Fatalf("page not served: %d", res.StatusCode)
	}
	if res.Header.Get("Content-Security-Policy") == "" {
		t.Error("missing CSP header")
	}
}

func TestRateLimit(t *testing.T) {
	_, srv, _ := newTestPhone(t)
	limited := false
	for i := 0; i < 40; i++ {
		if res := request(t, srv, "GET", "x", "", nil); res.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("burst of requests was never limited")
	}
}

func TestTokenPersistsAndRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	first, err := New(Config{Backend: &fakeBackend{}, TokenPath: path})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{Backend: &fakeBackend{}, TokenPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if first.currentToken() != second.currentToken() || len(first.currentToken()) < 20 {
		t.Fatalf("token not reused: %q vs %q", first.currentToken(), second.currentToken())
	}
	old := first.currentToken()
	if err := first.RotateToken(); err != nil {
		t.Fatal(err)
	}
	third, _ := New(Config{Backend: &fakeBackend{}, TokenPath: path})
	if first.currentToken() == old || third.currentToken() != first.currentToken() {
		t.Fatal("rotation not persisted")
	}
}

func running(s *Server) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

func TestEnableDisable(t *testing.T) {
	s, _, _ := newTestPhone(t)
	if running(s) {
		t.Fatal("starts disabled")
	}
	s.SetEnabled(true)
	s.SetEnabled(true)
	if !running(s) {
		t.Fatal("should be enabled")
	}
	s.Close()
	if running(s) || s.Info() != (Info{}) {
		t.Fatalf("should be disabled after close: %+v", s.Info())
	}
}

func TestStartFailureIsReported(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	s, err := New(Config{
		Logger:    slog.New(slog.DiscardHandler),
		Backend:   &fakeBackend{},
		Port:      busy.Addr().(*net.TCPAddr).Port,
		TokenPath: filepath.Join(t.TempDir(), "token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.SetEnabled(true)
	if info := s.Info(); !strings.Contains(info.Error, "could not start") {
		t.Fatalf("expected a start error, got %+v", info)
	}
	s.SetEnabled(false)
	if info := s.Info(); info.Error != "" {
		t.Fatalf("error should clear when switched off: %+v", info)
	}
}
