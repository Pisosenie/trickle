# Trickle

BitTorrent downloads for Steam Deck, right from Gaming Mode. Grab Linux images,
open-source games, mods or your own files without switching to Desktop Mode.

- Pauses or slows down while a game is running, picks up when you quit
- Can wait for the charger, stop below a battery level, or only run at night
- Send a magnet link from your phone: scan the QR code in the plugin
- Saves to internal storage or a microSD card
- Notifies you when a download is done

Everything runs in a single background service bundled with the plugin.
Nothing to install in Desktop Mode.

## Build

```sh
scripts/package.sh
```

Needs Go 1.24+, Node 18+ and `zip`. The result is `out/Trickle.zip`; install it
from Decky's developer settings.

Backend tests:

```sh
make -C backend test
```

## Layout

| Path | What |
|---|---|
| `backend/src` | `torrentd`, the download service (Go) |
| `main.py` | starts `torrentd` and passes calls from the UI |
| `src` | Quick Access panel (React) |

## License

MIT
