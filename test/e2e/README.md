# End-to-end tests

A fresh AutoThrottle, set up and used entirely through its web UI, against
real **Jellyfin 10.11** and **Emby 4.9** (each in turn), **qBittorrent 5.2**
and **Transmission 4.1** —
with checks made on the download clients themselves, not just on the page.

```sh
test/e2e/run.sh
```

Needs Docker and Python 3. The first run pulls about 2 GB of images and
makes a short test video; after that each media server takes about a minute.

| | |
|---|---|
| `MEDIA=jellyfin` or `MEDIA=emby` | run against one media server instead of both |
| `AT_IMAGE=<image>` | test an image you already built, instead of building from this checkout |
| `KEEP=1` | leave the stack running afterwards, on ports 17676 (AutoThrottle), 18196 (Jellyfin) or 18096 (Emby), 18180 and 18191 |

## What it covers

First-run setup · adding a media server, with a wrong and a
right key · refusing a duplicate · adding both download clients · the
mapping matrix · the idle Dashboard · playback starting, watched live with
no reloads, and the real limits on both clients · the timecode advancing ·
pause holding the throttle and the clock · turning throttling off and on ·
playback stopping and the limits restored · the log recording all of it ·
clearing the log and new events still arriving.

Plex isn't included: a Plex server can't be set up without a plex.tv
account to claim it.

## Files

- `compose.yml` — the stack
- `provision.py` — seeds it before start, and runs the media server's setup wizard after
- `scenario.js` — the browser scenario
- `reset.sh` / `run.sh` — rebuild the stack from nothing / run everything
