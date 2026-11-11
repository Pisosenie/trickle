import { Field, PanelSection, PanelSectionRow } from "@decky/ui";
import { api } from "../api";
import type { Settings } from "../types";
import { usePoll } from "../usePoll";
import { useSettings } from "../useSettings";
import { AddSection } from "./AddSection";
import { PhoneSection } from "./PhoneSection";
import { SettingsSection } from "./SettingsSection";
import { StatusBanner } from "./StatusBanner";
import { TorrentList } from "./TorrentList";

const POLL_MS = 1000;

export function Panel() {
  const overview = usePoll(api.overview, POLL_MS);
  const { settings, save } = useSettings(overview.data);
  const refresh = () => void overview.refresh();

  const saveSettings = async (next: Settings) => {
    await save(next);
    refresh();
  };

  if (overview.error && !overview.data) {
    return (
      <PanelSection>
        <PanelSectionRow>
          <Field label="Trickle is starting" description={overview.error} bottomSeparator="none" />
        </PanelSectionRow>
      </PanelSection>
    );
  }

  const list = overview.data?.torrents ?? [];
  return (
    <>
      <PanelSection>
        <StatusBanner status={overview.data?.status} torrents={list} />
      </PanelSection>
      <TorrentList torrents={list} onChange={refresh} />
      <AddSection startDir={settings?.download_dir ?? "/home"} onChange={refresh} />
      {settings && (
        <>
          <PhoneSection
            enabled={settings.phone_access}
            onToggle={(v) => saveSettings({ ...settings, phone_access: v })}
          />
          <SettingsSection settings={settings} onSave={saveSettings} />
        </>
      )}
    </>
  );
}
