import { Router } from "@decky/ui";
import { api } from "./api";

export function watchGames(): () => void {
  const running = new Set<number>();
  const initial = Router.MainRunningAppID;
  if (initial) running.add(initial);

  const send = () => {
    api.setGame(running.size > 0).catch(() => undefined);
  };

  const registration = SteamClient.GameSessions.RegisterForAppLifetimeNotifications((n) => {
    if (n.bRunning) {
      running.add(n.unAppID);
    } else {
      running.delete(n.unAppID);
    }
    send();
  });
  send();

  return () => registration.unregister();
}
