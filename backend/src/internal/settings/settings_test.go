package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	if err := Defaults("/home/deck/Downloads").Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	base := Defaults("/home/deck/Downloads")
	cases := map[string]func(s Settings) Settings{
		"relative dir":     func(s Settings) Settings { s.DownloadDir = "Downloads"; return s },
		"negative speed":   func(s Settings) Settings { s.DownloadLimitKBs = -1; return s },
		"unknown mode":     func(s Settings) Settings { s.GameMode = "turbo"; return s },
		"limit without kb": func(s Settings) Settings { s.GameMode = GameLimit; s.GameLimitKBs = 0; return s },
		"battery over 100": func(s Settings) Settings { s.MinBatteryPct = 101; return s },
		"bad start time":   func(s Settings) Settings { s.Schedule.Start = "25:00"; return s },
		"bad end format":   func(s Settings) Settings { s.Schedule.End = "7am"; return s },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if err := mutate(base).Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestMinuteOfDay(t *testing.T) {
	got, err := MinuteOfDay("23:59")
	if err != nil || got != 23*60+59 {
		t.Fatalf("MinuteOfDay(23:59) = %d, %v", got, err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	defaults := Defaults("/home/deck/Downloads")

	store, err := Open(path, defaults)
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	if store.Get() != defaults {
		t.Fatalf("fresh store should return defaults")
	}

	next := store.Get()
	next.DownloadLimitKBs = 2048
	next.GameMode = GameLimit
	if err := store.Replace(next); err != nil {
		t.Fatalf("replace: %v", err)
	}

	reopened, err := Open(path, defaults)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Get() != next {
		t.Fatalf("reopened = %+v, want %+v", reopened.Get(), next)
	}
}

func TestStoreRejectsInvalidReplace(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "settings.json"), Defaults("/d"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	bad := store.Get()
	bad.MinBatteryPct = -5
	if err := store.Replace(bad); err == nil {
		t.Fatal("expected validation error")
	}
	if store.Get().MinBatteryPct != 20 {
		t.Fatal("invalid settings must not become current")
	}
}

func TestOpenFailsOnBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, Defaults("/d")); err == nil {
		t.Fatal("expected parse error")
	}
}
