import { addEventListener, definePlugin, removeEventListener, toaster } from "@decky/api";
import { staticClasses } from "@decky/ui";
import { FaDownload } from "react-icons/fa";
import { Panel } from "./components/Panel";
import { watchGames } from "./game";

export default definePlugin(() => {
  const stopWatching = watchGames();
  const onCompleted = addEventListener<[name: string]>("torrent_completed", (name) => {
    toaster.toast({ title: "Download complete", body: name });
  });

  return {
    name: "Trickle",
    titleView: <div className={staticClasses.Title}>Trickle</div>,
    content: <Panel />,
    icon: <FaDownload />,
    onDismount() {
      stopWatching();
      removeEventListener("torrent_completed", onCompleted);
    },
  };
});
