package policy

import (
	"testing"
	"time"

	"trickle/internal/power"
	"trickle/internal/settings"
)

var (
	plugged   = power.Status{HasBattery: true, OnAC: true, Percent: 90}
	unplugged = power.Status{HasBattery: true, OnAC: false, Percent: 60}
	noon      = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
)

func base() settings.Settings {
	s := settings.Defaults("/home/deck/Downloads")
	s.GameMode = settings.GameIgnore
	s.MinBatteryPct = 0
	return s
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *settings.Settings)
		in     Inputs
		want   Decision
	}{
		{
			name: "idle and plugged runs unlimited",
			in:   Inputs{Power: plugged, Now: noon},
			want: Decision{Reasons: []Reason{}},
		},
		{
			name:   "user limits apply",
			mutate: func(s *settings.Settings) { s.DownloadLimitKBs = 100; s.UploadLimitKBs = 10 },
			in:     Inputs{Power: plugged, Now: noon},
			want:   Decision{DownloadLimit: 100 * 1024, UploadLimit: 10 * 1024, Reasons: []Reason{}},
		},
		{
			name:   "game pauses",
			mutate: func(s *settings.Settings) { s.GameMode = settings.GamePause },
			in:     Inputs{GameRunning: true, Power: plugged, Now: noon},
			want:   Decision{Paused: true, Reasons: []Reason{ReasonGame}},
		},
		{
			name: "game limit takes the stricter value",
			mutate: func(s *settings.Settings) {
				s.GameMode = settings.GameLimit
				s.GameLimitKBs = 200
				s.DownloadLimitKBs = 100
			},
			in:   Inputs{GameRunning: true, Power: plugged, Now: noon},
			want: Decision{DownloadLimit: 100 * 1024, UploadLimit: 200 * 1024, Reasons: []Reason{ReasonGame}},
		},
		{
			name:   "game mode ignore keeps going",
			mutate: func(s *settings.Settings) { s.GameMode = settings.GameIgnore },
			in:     Inputs{GameRunning: true, Power: plugged, Now: noon},
			want:   Decision{Reasons: []Reason{}},
		},
		{
			name:   "unplugged pauses when asked",
			mutate: func(s *settings.Settings) { s.PauseOnBattery = true },
			in:     Inputs{Power: unplugged, Now: noon},
			want:   Decision{Paused: true, Reasons: []Reason{ReasonOnBattery}},
		},
		{
			name:   "low battery pauses",
			mutate: func(s *settings.Settings) { s.MinBatteryPct = 70 },
			in:     Inputs{Power: unplugged, Now: noon},
			want:   Decision{Paused: true, Reasons: []Reason{ReasonLowBattery}},
		},
		{
			name:   "low battery is ignored while charging",
			mutate: func(s *settings.Settings) { s.MinBatteryPct = 95 },
			in:     Inputs{Power: plugged, Now: noon},
			want:   Decision{Reasons: []Reason{}},
		},
		{
			name: "outside schedule pauses",
			mutate: func(s *settings.Settings) {
				s.Schedule = settings.Schedule{Enabled: true, Start: "01:00", End: "07:00"}
			},
			in:   Inputs{Power: plugged, Now: noon},
			want: Decision{Paused: true, Reasons: []Reason{ReasonSchedule}},
		},
		{
			name: "all pause reasons are reported",
			mutate: func(s *settings.Settings) {
				s.GameMode = settings.GamePause
				s.PauseOnBattery = true
				s.MinBatteryPct = 70
			},
			in:   Inputs{GameRunning: true, Power: unplugged, Now: noon},
			want: Decision{Paused: true, Reasons: []Reason{ReasonGame, ReasonOnBattery, ReasonLowBattery}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			if tc.mutate != nil {
				tc.mutate(&s)
			}
			got := Evaluate(s, tc.in)
			if !got.Equal(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSeedFollowsSettings(t *testing.T) {
	s := base()
	s.SeedAfterDone = true
	if !Evaluate(s, Inputs{Power: plugged, Now: noon}).Seed {
		t.Fatal("seed flag should follow the setting")
	}
}

func TestInsideWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.UTC) }
	cases := []struct {
		start, end string
		now        time.Time
		want       bool
	}{
		{"01:00", "07:00", at(3, 0), true},
		{"01:00", "07:00", at(7, 0), false},
		{"01:00", "07:00", at(0, 59), false},
		{"23:00", "07:00", at(23, 30), true},
		{"23:00", "07:00", at(6, 59), true},
		{"23:00", "07:00", at(12, 0), false},
		{"00:00", "00:00", at(12, 0), true},
	}
	for _, tc := range cases {
		got := insideWindow(settings.Schedule{Enabled: true, Start: tc.start, End: tc.end}, tc.now)
		if got != tc.want {
			t.Errorf("%s-%s at %s: got %v, want %v", tc.start, tc.end, tc.now.Format("15:04"), got, tc.want)
		}
	}
}

func TestTighter(t *testing.T) {
	if tighter(0, 5) != 5 || tighter(5, 0) != 5 || tighter(3, 5) != 3 || tighter(0, 0) != 0 {
		t.Fatal("tighter is wrong")
	}
}
