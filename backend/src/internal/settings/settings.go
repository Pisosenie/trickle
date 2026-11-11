package settings

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

type GameMode string

const (
	GameIgnore GameMode = "ignore"
	GamePause  GameMode = "pause"
	GameLimit  GameMode = "limit"
)

type Schedule struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"` // "HH:MM"
	End     string `json:"end"`   // "HH:MM"
}

type Settings struct {
	DownloadDir      string   `json:"download_dir"`
	DownloadLimitKBs int      `json:"download_limit_kbs"`
	UploadLimitKBs   int      `json:"upload_limit_kbs"`
	GameMode         GameMode `json:"game_mode"`
	GameLimitKBs     int      `json:"game_limit_kbs"`
	PauseOnBattery   bool     `json:"pause_on_battery"`
	MinBatteryPct    int      `json:"min_battery_pct"`
	Schedule         Schedule `json:"schedule"`
	SeedAfterDone    bool     `json:"seed_after_done"`
	PhoneAccess      bool     `json:"phone_access"`
}

const maxSpeedKBs = 1 << 20

func Defaults(downloadDir string) Settings {
	return Settings{
		DownloadDir:    downloadDir,
		GameMode:       GamePause,
		GameLimitKBs:   512,
		PauseOnBattery: false,
		MinBatteryPct:  20,
		Schedule:       Schedule{Enabled: false, Start: "01:00", End: "07:00"},
		SeedAfterDone:  false,
		PhoneAccess:    false,
	}
}

func (s Settings) Validate() error {
	var errs []error
	if !filepath.IsAbs(s.DownloadDir) {
		errs = append(errs, fmt.Errorf("download_dir must be an absolute path"))
	}
	errs = appendSpeedErr(errs, "download_limit_kbs", s.DownloadLimitKBs)
	errs = appendSpeedErr(errs, "upload_limit_kbs", s.UploadLimitKBs)
	errs = appendSpeedErr(errs, "game_limit_kbs", s.GameLimitKBs)

	switch s.GameMode {
	case GameIgnore, GamePause, GameLimit:
	default:
		errs = append(errs, fmt.Errorf("game_mode must be ignore, pause or limit"))
	}
	if s.GameMode == GameLimit && s.GameLimitKBs == 0 {
		errs = append(errs, fmt.Errorf("game_limit_kbs must be above 0 in limit mode"))
	}
	if s.MinBatteryPct < 0 || s.MinBatteryPct > 100 {
		errs = append(errs, fmt.Errorf("min_battery_pct must be between 0 and 100"))
	}
	if _, err := MinuteOfDay(s.Schedule.Start); err != nil {
		errs = append(errs, fmt.Errorf("schedule.start: %w", err))
	}
	if _, err := MinuteOfDay(s.Schedule.End); err != nil {
		errs = append(errs, fmt.Errorf("schedule.end: %w", err))
	}
	return errors.Join(errs...)
}

func appendSpeedErr(errs []error, field string, kbs int) []error {
	if kbs < 0 || kbs > maxSpeedKBs {
		return append(errs, fmt.Errorf("%s must be between 0 and %d", field, maxSpeedKBs))
	}
	return errs
}

func MinuteOfDay(hhmm string) (int, error) {
	at, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid HH:MM time", hhmm)
	}
	return at.Hour()*60 + at.Minute(), nil
}
