import { Field, PanelSection, PanelSectionRow, ProgressBarWithInfo } from "@decky/ui";
import { eta, percent, speed } from "../format";
import type { Torrent, TorrentState } from "../types";
import { showTorrentActions } from "./TorrentActions";

const STATE_TEXT: Record<TorrentState, string> = {
  metadata: "Getting file list",
  downloading: "Downloading",
  waiting: "Waiting",
  paused: "Paused",
  seeding: "Seeding",
  done: "Done",
};

function operation(t: Torrent): string {
  if (t.state !== "downloading") return STATE_TEXT[t.state];
  const parts = [percent(t.progress), speed(t.down_rate), eta(t.eta_seconds)];
  return parts.filter(Boolean).join(" · ");
}

export function TorrentList({ torrents, onChange }: { torrents: Torrent[]; onChange: () => void }) {
  return (
    <PanelSection title="Downloads">
      {torrents.length === 0 && (
        <PanelSectionRow>
          <Field description="Add a magnet link below, or send one from your phone." bottomSeparator="none" />
        </PanelSectionRow>
      )}
      {torrents.map((t) => (
        <PanelSectionRow key={t.id}>
          <Field
            label={t.name}
            childrenLayout="below"
            focusable
            highlightOnFocus
            onActivate={() => showTorrentActions(t, onChange)}
            onClick={() => showTorrentActions(t, onChange)}
          >
            <ProgressBarWithInfo
              nProgress={t.progress * 100}
              indeterminate={t.state === "metadata"}
              sOperationText={operation(t)}
            />
          </Field>
        </PanelSectionRow>
      ))}
    </PanelSection>
  );
}
