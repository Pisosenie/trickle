package phone

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"golang.org/x/time/rate"

	"trickle/internal/engine"
	"trickle/internal/secret"
)

type Backend interface {
	Torrents() []engine.Torrent
	AddMagnet(uri string) (engine.Torrent, error)
	AddTorrentFile(data []byte) (engine.Torrent, error)
}

type Info struct {
	QRPNGBase64 string `json:"qr_png_base64,omitempty"`
	Error       string `json:"error,omitempty"`
}

type Config struct {
	Logger    *slog.Logger
	Backend   Backend
	Port      int
	TokenPath string
}

type Server struct {
	cfg     Config
	limiter *rate.Limiter

	mu       sync.Mutex
	token    string
	srv      *http.Server
	startErr error
}

func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	token, err := secret.LoadOrCreate(cfg.TokenPath)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:     cfg,
		limiter: rate.NewLimiter(5, 20),
		token:   token,
	}, nil
}

func (s *Server) SetEnabled(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case enabled && s.srv == nil:
		s.startLocked()
	case !enabled && s.srv != nil:
		s.stopLocked()
	case !enabled:
		s.startErr = nil
	}
}

func (s *Server) startLocked() {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(s.cfg.Port))
	if err != nil {
		if s.startErr == nil || s.startErr.Error() != err.Error() {
			s.cfg.Logger.Warn("phone page not started", "port", s.cfg.Port, "err", err)
		}
		s.startErr = err
		return
	}
	s.startErr = nil
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.srv = srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.cfg.Logger.Error("phone page stopped", "err", err)
		}
	}()
	s.cfg.Logger.Info("phone page enabled", "port", s.cfg.Port)
}

func (s *Server) stopLocked() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		s.cfg.Logger.Warn("phone page shutdown", "err", err)
	}
	s.srv = nil
	s.cfg.Logger.Info("phone page disabled")
}

func (s *Server) Close() {
	s.SetEnabled(false)
}

func (s *Server) RotateToken() error {
	token, err := secret.New()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := secret.Save(s.cfg.TokenPath, token); err != nil {
		return err
	}
	s.token = token
	return nil
}

func (s *Server) Info() Info {
	s.mu.Lock()
	running := s.srv != nil
	startErr := s.startErr
	token := s.token
	s.mu.Unlock()

	if startErr != nil {
		return Info{Error: fmt.Sprintf("phone page could not start: %v", startErr)}
	}
	if !running {
		return Info{}
	}
	ip, err := lanIPv4()
	if err != nil {
		return Info{Error: err.Error()}
	}
	// token in the fragment never reaches the server or its logs
	url := fmt.Sprintf("http://%s/#t=%s", net.JoinHostPort(ip.String(), strconv.Itoa(s.cfg.Port)), token)
	png, err := qrcode.Encode(url, qrcode.Medium, 320)
	if err != nil {
		return Info{Error: "cannot draw QR code"}
	}
	return Info{QRPNGBase64: base64.StdEncoding.EncodeToString(png)}
}

func (s *Server) currentToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}
