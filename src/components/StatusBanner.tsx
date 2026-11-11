import { Field, PanelSectionRow } from "@decky/ui";
import { bytes, speed } from "../format";
import type { Reason, Status, Torrent } from "../types";

const REASONS: Record<Reason, string> = {
  game: "a game is running",
  on_battery: "not plugged in",
  low_battery: "battery is low",
  schedule: "outside download hours",
};

function headline(status: Status): string {
  const reasons = status.decision.reasons.map((r) => REASONS[r]).join(", ");
  if (status.decision.paused) return `Paused: ${reasons}`;
  if (status.decision.reasons.includes("game")) return "Slowed down while you play";
  return "Active";
}

export function StatusBanner({ status, torrents }: { status?: Status; torrents: Torrent[] }) {
  if (!status) return null;
  const down = torrents.reduce((sum, t) => sum + t.down_rate, 0);
  const up = torrents.reduce((sum, t) => sum + t.up_rate, 0);
  return (
    <>
      <PanelSectionRow>
        <Field label={headline(status)} bottomSeparator="none">
          {down > 0 || up > 0 ? `↓ ${speed(down)}  ↑ ${speed(up)}` : ""}
        </Field>
      </PanelSectionRow>
      {status.total_bytes > 0 && (
        <PanelSectionRow>
          <Field label="Free space" bottomSeparator="none">
            {bytes(status.free_bytes)}
          </Field>
        </PanelSectionRow>
      )}
    </>
  );
}
