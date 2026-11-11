package engine

import (
	"time"

	"github.com/anacrolix/torrent"
)

type State string

const (
	StateMetadata    State = "metadata"
	StateDownloading State = "downloading"
	StateWaiting     State = "waiting"
	StatePaused      State = "paused"
	StateSeeding     State = "seeding"
	StateDone        State = "done"
)

type Torrent struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	State      State     `json:"state"`
	SizeBytes  int64     `json:"size_bytes"`
	DoneBytes  int64     `json:"done_bytes"`
	Progress   float64   `json:"progress"`
	DownRate   int64     `json:"down_rate"`
	UpRate     int64     `json:"up_rate"`
	Peers      int       `json:"peers"`
	ETASeconds int64     `json:"eta_seconds"`
	SaveDir    string    `json:"save_dir"`
	AddedAt    time.Time `json:"added_at"`
}

type meter struct {
	at       time.Time
	read     int64
	written  int64
	downRate int64
	upRate   int64
}

const minInterval = 900 * time.Millisecond

func (m meter) sample(now time.Time, read, written int64) meter {
	if m.at.IsZero() {
		return meter{at: now, read: read, written: written}
	}
	elapsed := now.Sub(m.at)
	if elapsed < minInterval {
		return m
	}
	seconds := elapsed.Seconds()
	return meter{
		at:       now,
		read:     read,
		written:  written,
		downRate: int64(float64(max(read-m.read, 0)) / seconds),
		upRate:   int64(float64(max(written-m.written, 0)) / seconds),
	}
}

type item struct {
	t     *torrent.Torrent
	rec   record
	meter meter
}

func (it item) finished() bool {
	return it.t.Complete().Bool()
}

type viewContext struct {
	now          time.Time
	globalPaused bool
	seeding      bool
}

func (it item) snapshot(ctx viewContext) (Torrent, item) {
	stats := it.t.Stats()
	nextMeter := it.meter.sample(ctx.now, stats.BytesReadUsefulData.Int64(), stats.BytesWrittenData.Int64())
	next := it
	next.meter = nextMeter

	view := Torrent{
		ID:         it.rec.InfoHash,
		Name:       it.t.Name(),
		Peers:      stats.ActivePeers,
		DownRate:   nextMeter.downRate,
		UpRate:     nextMeter.upRate,
		ETASeconds: -1,
		SaveDir:    it.rec.SaveDir,
		AddedAt:    it.rec.AddedAt,
	}

	hasInfo := it.t.Info() != nil
	complete := it.finished()
	if hasInfo {
		view.SizeBytes = it.t.Length()
		view.DoneBytes = it.t.BytesCompleted()
		if view.SizeBytes > 0 {
			view.Progress = float64(view.DoneBytes) / float64(view.SizeBytes)
		}
		if view.DownRate > 0 && !complete {
			view.ETASeconds = (view.SizeBytes - view.DoneBytes) / view.DownRate
		}
	}
	view.State = stateOf(it.rec.Paused, hasInfo, complete, ctx)
	return view, next
}

func stateOf(userPaused, hasInfo, complete bool, ctx viewContext) State {
	switch {
	case userPaused:
		return StatePaused
	case complete && ctx.seeding && !ctx.globalPaused:
		return StateSeeding
	case complete:
		return StateDone
	case ctx.globalPaused:
		return StateWaiting
	case !hasInfo:
		return StateMetadata
	default:
		return StateDownloading
	}
}
