package power

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func supply(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, content := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(content + "\n")}
	}
	return fsys
}

func TestRead(t *testing.T) {
	cases := []struct {
		name string
		fsys fstest.MapFS
		want Status
	}{
		{
			name: "deck on battery",
			fsys: supply(map[string]string{
				"ACAD/type": "Mains", "ACAD/online": "0",
				"BAT1/type": "Battery", "BAT1/capacity": "57", "BAT1/status": "Discharging",
			}),
			want: Status{HasBattery: true, OnAC: false, Percent: 57},
		},
		{
			name: "deck charging",
			fsys: supply(map[string]string{
				"ACAD/type": "Mains", "ACAD/online": "1",
				"BAT1/type": "Battery", "BAT1/capacity": "80", "BAT1/status": "Charging",
			}),
			want: Status{HasBattery: true, OnAC: true, Percent: 80},
		},
		{
			name: "full battery without mains entry",
			fsys: supply(map[string]string{
				"BAT0/type": "Battery", "BAT0/capacity": "100", "BAT0/status": "Full",
			}),
			want: Status{HasBattery: true, OnAC: true, Percent: 100},
		},
		{
			name: "no battery",
			fsys: supply(map[string]string{"ACAD/type": "Mains", "ACAD/online": "1"}),
			want: Status{HasBattery: false, OnAC: true},
		},
		{
			name: "garbage capacity is ignored",
			fsys: supply(map[string]string{
				"BAT1/type": "Battery", "BAT1/capacity": "n/a", "BAT1/status": "Discharging",
			}),
			want: Status{HasBattery: false, OnAC: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(tc.fsys)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadMissingDirMeansMains(t *testing.T) {
	got, err := Read(os.DirFS(filepath.Join(t.TempDir(), "missing")))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != (Status{OnAC: true}) {
		t.Fatalf("got %+v", got)
	}
}
