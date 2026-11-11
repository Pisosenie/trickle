import { DropdownItem, PanelSection, PanelSectionRow, SliderField, ToggleField } from "@decky/ui";
import { useEffect, useState } from "react";
import { api } from "../api";
import { bytes } from "../format";
import type { GameMode, Location, Settings } from "../types";

const SPEEDS = [0, 256, 512, 1024, 2048, 5120, 10240, 20480, 51200];

const speedOptions = SPEEDS.map((kbs) => ({
  data: kbs,
  label: kbs === 0 ? "Unlimited" : kbs < 1024 ? `${kbs} KB/s` : `${kbs / 1024} MB/s`,
}));

const gameOptions: { data: GameMode; label: string }[] = [
  { data: "pause", label: "Pause downloads" },
  { data: "limit", label: "Slow down" },
  { data: "ignore", label: "Keep going" },
];

const hourOptions = Array.from({ length: 24 }, (_, h) => {
  const value = `${String(h).padStart(2, "0")}:00`;
  return { data: value, label: value };
});

interface Props {
  settings: Settings;
  onSave: (next: Settings) => Promise<void>;
}

function useLocations(current: string) {
  const [locations, setLocations] = useState<Location[]>([]);
  useEffect(() => {
    api.locations().then(setLocations).catch(() => setLocations([]));
  }, []);
  const known = locations.some((l) => l.path === current);
  const options = locations.map((l) => ({ data: l.path, label: `${l.label} · ${bytes(l.free_bytes)} free` }));
  return known ? options : [{ data: current, label: current }, ...options];
}

export function SettingsSection({ settings, onSave }: Props) {
  const folders = useLocations(settings.download_dir);
  const set = (patch: Partial<Settings>) => onSave({ ...settings, ...patch });
  const setSchedule = (patch: Partial<Settings["schedule"]>) =>
    onSave({ ...settings, schedule: { ...settings.schedule, ...patch } });

  return (
    <PanelSection title="Settings">
      <PanelSectionRow>
        <DropdownItem
          label="Save to"
          rgOptions={folders}
          selectedOption={settings.download_dir}
          onChange={(o) => set({ download_dir: o.data })}
        />
      </PanelSectionRow>
      <PanelSectionRow>
        <DropdownItem
          label="While playing"
          rgOptions={gameOptions}
          selectedOption={settings.game_mode}
          onChange={(o) => set({ game_mode: o.data })}
        />
      </PanelSectionRow>
      {settings.game_mode === "limit" && (
        <PanelSectionRow>
          <DropdownItem
            label="Speed while playing"
            rgOptions={speedOptions.filter((o) => o.data > 0)}
            selectedOption={settings.game_limit_kbs}
            onChange={(o) => set({ game_limit_kbs: o.data })}
          />
        </PanelSectionRow>
      )}
      <PanelSectionRow>
        <ToggleField
          label="Only when plugged in"
          checked={settings.pause_on_battery}
          onChange={(v) => set({ pause_on_battery: v })}
        />
      </PanelSectionRow>
      <PanelSectionRow>
        <SliderField
          label="Pause below battery"
          value={settings.min_battery_pct}
          min={0}
          max={50}
          step={5}
          showValue
          valueSuffix="%"
          description={settings.min_battery_pct === 0 ? "Off" : undefined}
          onChange={(v) => set({ min_battery_pct: v })}
        />
      </PanelSectionRow>
      <PanelSectionRow>
        <ToggleField
          label="Download hours"
          checked={settings.schedule.enabled}
          onChange={(v) => setSchedule({ enabled: v })}
        />
      </PanelSectionRow>
      {settings.schedule.enabled && (
        <>
          <PanelSectionRow>
            <DropdownItem
              label="From"
              rgOptions={hourOptions}
              selectedOption={settings.schedule.start}
              onChange={(o) => setSchedule({ start: o.data })}
            />
          </PanelSectionRow>
          <PanelSectionRow>
            <DropdownItem
              label="Until"
              rgOptions={hourOptions}
              selectedOption={settings.schedule.end}
              onChange={(o) => setSchedule({ end: o.data })}
            />
          </PanelSectionRow>
        </>
      )}
      <PanelSectionRow>
        <DropdownItem
          label="Download speed"
          rgOptions={speedOptions}
          selectedOption={settings.download_limit_kbs}
          onChange={(o) => set({ download_limit_kbs: o.data })}
        />
      </PanelSectionRow>
      <PanelSectionRow>
        <DropdownItem
          label="Upload speed"
          rgOptions={speedOptions}
          selectedOption={settings.upload_limit_kbs}
          onChange={(o) => set({ upload_limit_kbs: o.data })}
        />
      </PanelSectionRow>
      <PanelSectionRow>
        <ToggleField
          label="Keep sharing when done"
          description="Uploads finished files to others until you pause them."
          checked={settings.seed_after_done}
          onChange={(v) => set({ seed_after_done: v })}
        />
      </PanelSectionRow>
    </PanelSection>
  );
}
