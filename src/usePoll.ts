import { useQuickAccessVisible } from "@decky/api";
import { useCallback, useEffect, useRef, useState } from "react";
import { errorText } from "./format";

export interface Poll<T> {
  data?: T;
  error?: string;
  refresh: () => void;
}

export function usePoll<T>(load: () => Promise<T>, intervalMs: number): Poll<T> {
  const visible = useQuickAccessVisible();
  const [data, setData] = useState<T>();
  const [error, setError] = useState<string>();
  const loadRef = useRef(load);
  loadRef.current = load;

  const refresh = useCallback(
    () =>
      loadRef
        .current()
        .then((next) => {
          setData(next);
          setError(undefined);
        })
        .catch((err) => setError(errorText(err))),
    [],
  );

  useEffect(() => {
    if (!visible) return undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let active = true;
    const tick = () => {
      refresh().finally(() => {
        if (active) timer = setTimeout(tick, intervalMs);
      });
    };
    tick();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [visible, intervalMs, refresh]);

  return { data, error, refresh };
}
