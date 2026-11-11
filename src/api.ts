import { callable } from "@decky/api";
import type { Location, Overview, PhoneInfo, Settings, Torrent } from "./types";

interface Envelope<T> {
  success: boolean;
  data?: T;
  error?: string;
}

type Method = "GET" | "POST" | "PUT" | "DELETE";

const call = callable<[method: Method, path: string, body?: unknown], Envelope<unknown>>("api");

async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  const res = await call(method, "/api/v1" + path, body);
  if (!res.success) {
    throw new Error(res.error ?? "request failed");
  }
  return res.data as T;
}

const torrentPath = (id: string) => "/torrents/" + encodeURIComponent(id);

export const api = {
  overview: () => request<Overview>("GET", "/overview"),
  addMagnet: (magnet: string) => request<Torrent>("POST", "/torrents", { magnet }),
  addFile: (path: string) => request<Torrent>("POST", "/torrents", { torrent_path: path }),
  pause: (id: string) => request<null>("POST", torrentPath(id) + "/pause"),
  resume: (id: string) => request<null>("POST", torrentPath(id) + "/resume"),
  remove: (id: string, deleteFiles: boolean) =>
    request<null>("DELETE", torrentPath(id) + (deleteFiles ? "?delete_files=true" : "")),
  settings: () => request<Settings>("GET", "/settings"),
  saveSettings: (next: Settings) => request<Settings>("PUT", "/settings", next),
  locations: () => request<Location[]>("GET", "/locations"),
  phone: () => request<PhoneInfo>("GET", "/phone"),
  newPhoneCode: () => request<PhoneInfo>("POST", "/phone/token"),
  setGame: callable<[running: boolean], void>("set_game"),
};
