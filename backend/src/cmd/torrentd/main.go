package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"trickle/internal/api"
	"trickle/internal/disk"
	"trickle/internal/engine"
	"trickle/internal/events"
	"trickle/internal/httpx"
	"trickle/internal/phone"
	"trickle/internal/power"
	"trickle/internal/secret"
	"trickle/internal/service"
	"trickle/internal/settings"
)

var version = "dev"

type options struct {
	stateDir    string
	downloadDir string
	apiAddr     string
	phonePort   int
	peerPort    int
	mediaRoot   string
	powerDir    string
	withParent  bool
	gameRunning bool
}

func parseFlags() (options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return options{}, fmt.Errorf("find home dir: %w", err)
	}
	var o options
	flag.StringVar(&o.stateDir, "state-dir", filepath.Join(home, ".local", "share", "trickle"), "where the torrent list and settings live")
	flag.StringVar(&o.downloadDir, "download-dir", filepath.Join(home, "Downloads"), "default download folder for a fresh install")
	flag.StringVar(&o.apiAddr, "api-addr", "127.0.0.1:47610", "local API address (loopback only)")
	flag.IntVar(&o.phonePort, "phone-port", 47611, "port of the phone page on the local network")
	flag.IntVar(&o.peerPort, "peer-port", 47620, "BitTorrent listen port")
	flag.StringVar(&o.mediaRoot, "media-root", disk.MediaRoot, "where removable drives are mounted")
	flag.StringVar(&o.powerDir, "power-dir", power.SysfsDir, "sysfs power_supply directory")
	flag.BoolVar(&o.withParent, "exit-with-parent", false, "exit when stdin closes; the parent holds the other end")
	flag.BoolVar(&o.gameRunning, "game-running", false, "whether a game is running at startup")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}
	if !httpx.IsLoopbackHost(o.apiAddr) {
		return options{}, fmt.Errorf("-api-addr must be a loopback address, got %q", o.apiAddr)
	}
	return o, nil
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("torrentd failed", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	opts, err := parseFlags()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if opts.withParent {
		go exitOnStdinEOF(log, stop)
	}

	store, err := settings.Open(filepath.Join(opts.stateDir, "settings.json"), settings.Defaults(opts.downloadDir))
	if err != nil {
		return err
	}

	apiToken, err := secret.LoadOrCreate(filepath.Join(opts.stateDir, "api-token"))
	if err != nil {
		return err
	}

	hub := events.NewHub()
	eng, err := engine.New(engine.Config{
		StateDir: filepath.Join(opts.stateDir, "engine"),
		PeerPort: opts.peerPort,
		Logger:   log,
		OnComplete: func(t engine.Torrent) {
			hub.Publish(events.Event{Type: events.TorrentCompleted, Data: t})
		},
	})
	if err != nil {
		return err
	}
	defer closeLogged(log, "engine", eng.Close)

	powerFS := os.DirFS(opts.powerDir)
	svc := service.New(service.Deps{
		Logger:      log,
		Settings:    store,
		Engine:      eng,
		ReadPower:   func() (power.Status, error) { return power.Read(powerFS) },
		InternalDir: opts.downloadDir,
		MediaRoot:   opts.mediaRoot,
		GameRunning: opts.gameRunning,
	})

	ph, err := phone.New(phone.Config{
		Logger:    log,
		Backend:   svc,
		Port:      opts.phonePort,
		TokenPath: filepath.Join(opts.stateDir, "phone-token"),
	})
	if err != nil {
		return err
	}
	defer ph.Close()
	svc.AttachPhone(ph)

	go svc.Run(ctx)
	log.Info("torrentd started", "version", version, "api", opts.apiAddr, "state", opts.stateDir)
	handler := api.NewHandler(api.Deps{Logger: log, Backend: svc, Phone: ph, Hub: hub, Token: apiToken})
	return serveAPI(ctx, log, opts.apiAddr, handler)
}

func exitOnStdinEOF(log *slog.Logger, stop context.CancelFunc) {
	_, _ = io.Copy(io.Discard, os.Stdin)
	log.Info("parent closed stdin, exiting")
	stop()
}

func serveAPI(ctx context.Context, log *slog.Logger, addr string, handler http.Handler) error {
	streams, cancelStreams := context.WithCancel(context.Background())
	defer cancelStreams()
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return streams },
	}
	srv.RegisterOnShutdown(cancelStreams)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		return fmt.Errorf("local API: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func closeLogged(log *slog.Logger, what string, closeFn func() error) {
	if err := closeFn(); err != nil {
		log.Warn("close failed", "what", what, "err", err)
	}
}
