# End-to-end tests

These tests set up a fresh AutoThrottle and use it only through its web UI,
against real servers:

- Jellyfin 10.11, Emby 4.9 and Plex 1.43, one at a time
- qBittorrent 5.2 and Transmission 4.1

Throttling is checked on the download clients themselves, not only on the
page.

## Running

```sh
test/e2e/run.sh
```

You need Docker and Python 3. The first run pulls about 2 GB of images.
After that, each media server takes about two minutes.

| Option | Effect |
|---|---|
| `MEDIA=jellyfin`, `emby` or `plex` | Run one media server instead of all three. |
| `AT_IMAGE=<image>` | Test an image you already built instead of building this checkout. |
| `KEEP=1` | Leave the stack running when the run ends. |
| `E2E_NEWEST=1` | Run each server at its newest release instead of the pinned version. |
| `PLEX_E2E_ENV=<file>` | Read the Plex test account's details from this file. |

With `KEEP=1`, the stack is on these ports:

| Service | Port |
|---|---|
| AutoThrottle | 17676 |
| Jellyfin | 18196 |
| Emby | 18096 |
| Plex | 18324 |
| qBittorrent | 18180 |
| Transmission | 18191 |

## What it covers

**Setup**
- Creating the account on first start-up
- Adding a media server, first with a wrong credential and then the right one
- Signing in with Plex (Plex only)
- Refusing a duplicate media server
- Adding qBittorrent and Transmission
- Turning mappings off and on

**Throttling**
- Playback throttling both download clients, with the limits checked on each client
- The Dashboard showing the throttle within 1.5 seconds, without a reload
- The playback position and session timer advancing
- A paused session keeping the throttle on
- Throttling being restored after AutoThrottle restarts mid-playback
- Disabling and resuming throttling from the banner
- Default rates coming back when playback stops
- Local playback being ignored when that option is on

**Recovering from outages**
- A download client restarting, and AutoThrottle reapplying its rates once it is back
- A media server going down, and the Dashboard showing it reconnecting

**Live updates**
- The Dashboard updating over the event stream, and not polling while it is open
- Falling back to polling when the stream is blocked
- A background tab closing its stream and catching up when it comes back

**Everything else**
- The log, including clearing it
- Deleting a connection
- The 404 page
- Returning to the page you asked for after signing in

## In CI

The suite runs nightly, on demand from the Actions tab, and on pull
requests that change what it tests, such as dependency updates. Nightly,
it also runs against each server's newest release. While that fails, one
issue labelled `upstream` stays open with the versions that failed, and it
closes itself once a run passes.

## Plex

Plex needs a real plex.tv account to claim the server, so it uses an account
set aside for testing. Each run claims a new server. When the run ends, pass
or fail, it removes that server and the AutoThrottle instances that signed
in from the account.

The run needs the account's token and a client identifier:

- In CI, these are the `PLEX_E2E_TOKEN` and `PLEX_E2E_CLIENT_ID` repository
  secrets.
- Locally, export them, or put them in a file of `KEY=value` lines outside
  the repository and set `PLEX_E2E_ENV` to its path.

If they aren't set, the Plex run is skipped. The token is never written to
disk.

## Files

| File | Purpose |
|---|---|
| `compose.yml` | The stack, with each server pinned to a tested version |
| `compose.newest.yml` | Each server at its newest release, for `E2E_NEWEST=1` |
| `provision.py` | Prepares the stack before it starts and sets up the media server after |
| `scenario.js` | The browser test |
| `run.sh` | Runs everything |
| `reset.sh` | Rebuilds the stack from nothing |
| `fixtures/` | The test video, a 200 KB, ten-minute H.264 file |
