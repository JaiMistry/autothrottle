# End-to-end tests

A fresh AutoThrottle, set up and used entirely through its web UI, against
real **Jellyfin 10.11**, **Emby 4.9** and **Plex 1.43** (each in turn),
**qBittorrent 5.2** and **Transmission 4.1** —
with checks made on the download clients themselves, not just on the page.

```sh
test/e2e/run.sh
```

Needs Docker and Python 3. The first run pulls about 2 GB of images; after
that each media server takes about two minutes. The test video is committed
(`fixtures/`, a 200 KB, ten-minute H.264 file), so nothing is rendered.

| | |
|---|---|
| `MEDIA=jellyfin`, `MEDIA=emby` or `MEDIA=plex` | run against one media server instead of all |
| `AT_IMAGE=<image>` | test an image you already built, instead of building from this checkout |
| `KEEP=1` | leave the stack running afterwards, on ports 17676 (AutoThrottle), 18196 (Jellyfin), 18096 (Emby) or 18324 (Plex), 18180 and 18191 |
| `PLEX_E2E_ENV=<file>` | where to read the Plex test account's `PLEX_E2E_TOKEN` and `PLEX_E2E_CLIENT_ID` from, if they aren't in the environment |

## What it covers

Creating the account · adding a media server in the drawer, with a wrong
and a right credential · refusing a duplicate · both download clients ·
the mapping matrix · live updates over the event stream, and the fallback
poll when it is blocked · playback throttling both clients for real,
shown within 1.5s · the timecode and session timer · a restart
mid-playback · pause holding the throttle · disabling and resuming
throttling from the banner · a download client restarting and recovering
· the local-playback exemption · the log · deleting a connection · the
404 page and returning to a page after signing in.

With Plex, the run also covers **Sign in with Plex**: the request is
approved on plex.tv with the test account's token, as its owner would.

## Plex

Plex runs against a real plex.tv account set aside for testing, because a
Plex server can't be set up without one to claim it. Each run claims a
fresh server, and removes it from the account when it finishes — failed
or not — along with the AutoThrottle instances that signed in to it.

It needs the account's token and a client identifier:

- **In CI** they are the `PLEX_E2E_TOKEN` and `PLEX_E2E_CLIENT_ID`
  repository secrets.
- **Locally**, export them, or keep them in a file of `KEY=value` lines
  **outside the repository** and point `PLEX_E2E_ENV` at it.

Without them the Plex run is skipped. The token is never written to disk:
the scenario reads it from the environment.

## Files

- `compose.yml` — the stack
- `provision.py` — seeds it before start, and runs the media server's setup wizard after
- `scenario.js` — the browser scenario
- `reset.sh` / `run.sh` — rebuild the stack from nothing / run everything
