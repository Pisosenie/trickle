package power

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

const SysfsDir = "/sys/class/power_supply"

type Status struct {
	HasBattery bool `json:"has_battery"`
	OnAC       bool `json:"on_ac"`
	Percent    int  `json:"percent"`
}

func Read(fsys fs.FS) (Status, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return Status{OnAC: true}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("list power supplies: %w", err)
	}

	status := Status{}
	for _, entry := range entries {
		// sysfs entries are symlinks, so no IsDir check
		name := entry.Name()
		switch readAttr(fsys, name, "type") {
		case "Battery":
			status = withBattery(status, fsys, name)
		case "Mains", "USB", "USB_C", "USB_PD":
			if readAttr(fsys, name, "online") == "1" {
				status.OnAC = true
			}
		}
	}
	if !status.HasBattery {
		status.OnAC = true
	}
	return status, nil
}

func withBattery(status Status, fsys fs.FS, name string) Status {
	if status.HasBattery {
		return status
	}
	percent, err := strconv.Atoi(readAttr(fsys, name, "capacity"))
	if err != nil {
		return status
	}
	state := readAttr(fsys, name, "status")
	next := status
	next.HasBattery = true
	next.Percent = clamp(percent, 0, 100)
	if state == "Charging" || state == "Full" || state == "Not charging" {
		next.OnAC = true
	}
	return next
}

func readAttr(fsys fs.FS, supply, attr string) string {
	data, err := fs.ReadFile(fsys, path.Join(supply, attr))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
