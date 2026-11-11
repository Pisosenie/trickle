package policy

import (
	"slices"
	"time"

	"trickle/internal/power"
	"trickle/internal/settings"
)

type Reason string

const (
	ReasonGame       Reason = "game"
	ReasonOnBattery  Reason = "on_battery"
	ReasonLowBattery Reason = "low_battery"
	ReasonSchedule   Reason = "schedule"
)

type Inputs struct {
	GameRunning bool
	Power       power.Status
	Now         time.Time
}

type Decision struct {
	Paused        bool     `json:"paused"`
	DownloadLimit int64    `json:"download_limit"`
	UploadLimit   int64    `json:"upload_limit"`
	Seed          bool     `json:"seed"`
	Reasons       []Reason `json:"reasons"`
}

func Evaluate(s settings.Settings, in Inputs) Decision {
	var pauseReasons []Reason
	if in.GameRunning && s.GameMode == settings.GamePause {
		pauseReasons = append(pauseReasons, ReasonGame)
	}
	if in.Power.HasBattery && !in.Power.OnAC {
		if s.PauseOnBattery {
			pauseReasons = append(pauseReasons, ReasonOnBattery)
		}
		if s.MinBatteryPct > 0 && in.Power.Percent < s.MinBatteryPct {
			pauseReasons = append(pauseReasons, ReasonLowBattery)
		}
	}
	if s.Schedule.Enabled && !insideWindow(s.Schedule, in.Now) {
		pauseReasons = append(pauseReasons, ReasonSchedule)
	}
	if len(pauseReasons) > 0 {
		return Decision{Paused: true, Seed: s.SeedAfterDone, Reasons: pauseReasons}
	}

	download := kbs(s.DownloadLimitKBs)
	upload := kbs(s.UploadLimitKBs)
	if in.GameRunning && s.GameMode == settings.GameLimit {
		game := kbs(s.GameLimitKBs)
		return Decision{
			DownloadLimit: tighter(download, game),
			UploadLimit:   tighter(upload, game),
			Seed:          s.SeedAfterDone,
			Reasons:       []Reason{ReasonGame},
		}
	}
	return Decision{DownloadLimit: download, UploadLimit: upload, Seed: s.SeedAfterDone, Reasons: []Reason{}}
}

func (d Decision) Equal(other Decision) bool {
	return d.Paused == other.Paused && d.DownloadLimit == other.DownloadLimit &&
		d.UploadLimit == other.UploadLimit && d.Seed == other.Seed &&
		slices.Equal(d.Reasons, other.Reasons)
}

func insideWindow(sched settings.Schedule, now time.Time) bool {
	start, errStart := settings.MinuteOfDay(sched.Start)
	end, errEnd := settings.MinuteOfDay(sched.End)
	if errStart != nil || errEnd != nil || start == end {
		return true
	}
	minute := now.Hour()*60 + now.Minute()
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

func kbs(v int) int64 {
	return int64(v) * 1024
}

func tighter(a, b int64) int64 {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	default:
		return min(a, b)
	}
}
