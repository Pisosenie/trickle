import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { toastError } from "./toast";
import type { Settings } from "./types";

export function useSettings(retryTrigger: unknown) {
  const [settings, setSettings] = useState<Settings>();
  const confirmed = useRef<Settings | undefined>(undefined);
  const pending = useRef<Settings | undefined>(undefined);
  const drain = useRef<Promise<void> | undefined>(undefined);

  useEffect(() => {
    if (settings) return;
    api
      .settings()
      .then((loaded) => {
        confirmed.current = loaded;
        setSettings(loaded);
      })
      .catch(() => undefined);
  }, [settings, retryTrigger]);

  const flush = async () => {
    while (pending.current) {
      const target = pending.current;
      pending.current = undefined;
      try {
        confirmed.current = await api.saveSettings(target);
      } catch (err) {
        toastError("Settings not saved", err);
      }
    }
    setSettings(confirmed.current);
    drain.current = undefined;
  };

  const save = (next: Settings): Promise<void> => {
    setSettings(next);
    pending.current = next;
    drain.current ??= flush();
    return drain.current;
  };

  return { settings, save };
}
