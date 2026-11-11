import { ConfirmModal, Menu, MenuItem, showContextMenu, showModal } from "@decky/ui";
import { api } from "../api";
import { bytes } from "../format";
import { toastError } from "../toast";
import type { Torrent } from "../types";

async function run(action: () => Promise<unknown>, done: () => void) {
  try {
    await action();
  } catch (err) {
    toastError("Trickle", err);
  }
  done();
}

function confirmRemove(t: Torrent, deleteFiles: boolean, onChange: () => void) {
  showModal(
    <ConfirmModal
      strTitle={deleteFiles ? "Delete downloaded files?" : "Remove from list?"}
      strDescription={
        deleteFiles
          ? `${t.name} and its ${bytes(t.done_bytes)} of data will be deleted.`
          : `${t.name} stays on disk, it just leaves the list.`
      }
      strOKButtonText={deleteFiles ? "Delete" : "Remove"}
      onOK={() => run(() => api.remove(t.id, deleteFiles), onChange)}
    />,
  );
}

export function showTorrentActions(t: Torrent, onChange: () => void) {
  const paused = t.state === "paused";
  showContextMenu(
    <Menu label={t.name}>
      <MenuItem onSelected={() => run(() => (paused ? api.resume(t.id) : api.pause(t.id)), onChange)}>
        {paused ? "Resume" : "Pause"}
      </MenuItem>
      <MenuItem onSelected={() => confirmRemove(t, false, onChange)}>Remove from list</MenuItem>
      <MenuItem tone="destructive" onSelected={() => confirmRemove(t, true, onChange)}>
        Remove and delete files
      </MenuItem>
    </Menu>,
  );
}
