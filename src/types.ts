export type TorrentState = "metadata" | "downloading" | "waiting" | "paused" | "seeding" | "done";

export interface Torrent {
  id: string;
  name: string;
  state: TorrentState;
  size_bytes: number;
  done_bytes: number;
  progress: number;
  down_rate: number;
  up_rate: number;
  peers: number;
  eta_seconds: number;
  save_dir: string;
  added_at: string;
}

export type Reason = "game" | "on_battery" | "low_battery" | "schedule";

export interface Status {
  decision: {
    paused: boolean;
    download_limit: number;
    upload_limit: number;
    seed: boolean;
    reasons: Reason[];
  };
  free_bytes: number;
  total_bytes: number;
}

export interface Overview {
  status: Status;
  torrents: Torrent[];
}

export type GameMode = "ignore" | "pause" | "limit";

export interface Settings {
  download_dir: string;
  download_limit_kbs: number;
  upload_limit_kbs: number;
  game_mode: GameMode;
  game_limit_kbs: number;
  pause_on_battery: boolean;
  min_battery_pct: number;
  schedule: { enabled: boolean; start: string; end: string };
  seed_after_done: boolean;
  phone_access: boolean;
}

export interface Location {
  path: string;
  label: string;
  free_bytes: number;
}

export interface PhoneInfo {
  qr_png_base64?: string;
  error?: string;
}
