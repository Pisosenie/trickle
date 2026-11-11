const UNITS = ["B", "KB", "MB", "GB", "TB"];

export function bytes(n: number): string {
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  const digits = value >= 100 || unit === 0 ? 0 : 1;
  return `${value.toFixed(digits)} ${UNITS[unit]}`;
}

export function speed(bytesPerSec: number): string {
  return bytes(bytesPerSec) + "/s";
}

export function eta(seconds: number): string {
  if (seconds < 0) return "";
  if (seconds < 60) return "under a minute";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min left`;
  const hours = Math.floor(minutes / 60);
  return `${hours} h ${minutes % 60} min left`;
}

export function percent(progress: number): string {
  return `${(progress * 100).toFixed(progress >= 0.1 ? 0 : 1)}%`;
}

export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
