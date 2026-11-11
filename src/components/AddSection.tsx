import { FileSelectionType, openFilePicker, toaster } from "@decky/api";
import { ButtonItem, PanelSection, PanelSectionRow, TextField } from "@decky/ui";
import { useState } from "react";
import { api } from "../api";
import { toastError } from "../toast";
import type { Torrent } from "../types";

function added(t: Torrent) {
  toaster.toast({ title: "Added", body: t.name });
}

function failed(err: unknown) {
  toastError("Could not add", err);
}

export function AddSection({ startDir, onChange }: { startDir: string; onChange: () => void }) {
  const [magnet, setMagnet] = useState("");
  const [busy, setBusy] = useState(false);

  const addMagnet = async () => {
    setBusy(true);
    try {
      added(await api.addMagnet(magnet.trim()));
      setMagnet("");
      onChange();
    } catch (err) {
      failed(err);
    } finally {
      setBusy(false);
    }
  };

  const pickFile = async () => {
    try {
      const picked = await openFilePicker(FileSelectionType.FILE, startDir, true, true, undefined, ["torrent"]);
      added(await api.addFile(picked.realpath));
      onChange();
    } catch (err) {
      if (err) failed(err);
    }
  };

  return (
    <PanelSection title="Add">
      <PanelSectionRow>
        <TextField
          label="Magnet link"
          value={magnet}
          onChange={(e) => setMagnet(e.target.value)}
        />
      </PanelSectionRow>
      <PanelSectionRow>
        <ButtonItem layout="below" disabled={busy || !magnet.trim()} onClick={addMagnet}>
          Add magnet link
        </ButtonItem>
      </PanelSectionRow>
      <PanelSectionRow>
        <ButtonItem layout="below" onClick={pickFile}>
          Open .torrent file
        </ButtonItem>
      </PanelSectionRow>
    </PanelSection>
  );
}
