# End-to-end tests

These tests set up a fresh AutoThrottle and use it only through its web UI,
against real servers:

- Jellyfin, Emby and Plex, one at a time
- qBittorrent, Transmission and Deluge, all three with each media server

Each server runs at the oldest version the main README supports, and in a
second round at its newest release (see [In CI](#in-ci)). Throttling is
checked on the download clients themselves, not only on the page.

## Running

```sh
test/e2e/run.sh
```

You need Docker and Python 3. The first run pulls over 2 GB of images.
After that, each media server takes about four minutes.

| Option | Effect |
|---|---|
| `MEDIA=jellyfin`, `emby` or `plex` | Run one media server instead of all three. |
| `AT_IMAGE=<image>` | Test an image you already built instead of building this checkout. |
| `KEEP=1` | Leave the stack running when the run ends. |
| `E2E_NEWEST=1` | Run each server at its newest release instead of the pinned version. |
| `PLEX_E2E_ENV=<file>` | Read the Plex test account's details from this file. |
| `E2E_BROWSER=host` | Use this machine's Node, puppeteer-core and Chrome (`PUPPETEER_EXECUTABLE_PATH`) instead of the Puppeteer image. |

With `KEEP=1`, the stack is on these ports:

| Service | Port |
|---|---|
| AutoThrottle | 17676 |
| Jellyfin | 18196 |
| Emby | 18096 |
| Plex | 18324 |
| qBittorrent | 18180 |
| Transmission | 18191 |
| Deluge | 18112 |

## What it covers

**Setup**
- Creating the account on first start-up, and the empty Dashboard after
- Adding a media server, first with a wrong credential and then the right one
- Signing in with Plex (Plex only)
- Refusing a duplicate media server, both at the same address and under another address, by the ID the server reports
- Adding qBittorrent, Transmission and Deluge, with the address example showing each one's usual port
- Deluge asking for a password only, and a wrong one failing the test and blocking saving
- New mappings starting on, and turning them off and on

**Throttling**
- Docker's health check calling AutoThrottle healthy
- Playback throttling all three download clients, with the limits checked on each client
- The Dashboard showing the throttle within 1.5 seconds, without a reload, in its headline and the status beside the title
- The playback position and session timer advancing
- A paused session keeping the throttle on, with its timecode standing still
- Disabling throttling from the Dashboard and resuming from the banner, on any page, without nesting a second page
- Default rates coming back at once when a film stops
- Local playback being ignored when that option is on

**Between episodes**
- An episode stopped partway releasing at once
- One stopped near its end holding the throttle, with the Dashboard saying it's waiting on the next episode
- The next episode carrying the throttle on, with qBittorrent never restored in between
- A show's last episode releasing at once, with nothing to autoplay
- An episode whose next never starts releasing by itself, about 30 seconds after it ended

**Recovering from outages**
- AutoThrottle restarting mid-playback: the throttle re-applied, the browser still signed in, the open page reconnecting its stream, and the health check passing again
- qBittorrent and Deluge down while AutoThrottle starts: shown as Unreachable, logged once, and given their rates once they're back
- A restarted Deluge, whose web UI comes back disconnected from its daemon: the next playback still throttles it, and releases it after
- A media server going down, and the Dashboard showing it reconnecting

**Live updates**
- The Dashboard updating over the event stream, and not polling while it is open
- Falling back to polling when the stream is blocked
- A background tab closing its stream and catching up when it comes back

**Editing**
- Which changes need a new connection test: an address, username or password does; a name, rates or the local playback switch don't

**Everything else**
- The log, with each throttle and restore and its cause, clearing it, and new events appearing live
- Deleting a connection, after a confirmation naming what goes
- Moving between pages, and each tab named after its page
- The 404 page, and its entry in the log
- A dead link while signed out going to the sign-in page, and a real one returning to that page after signing in
- No JavaScript errors on any page

## In CI

The suite runs nightly, on demand from the Actions tab, and on pull
requests that change what it tests, such as dependency updates. Each
media server is its own job, in two rounds:

- **Pinned** (`compose.yml`): each server at the oldest version the main
  README supports, so its "4.9+" is tested, not assumed. The pins move only
  when the README's minimums do, and `pins_test.go` fails CI until they
  match.
- **Newest** (`compose.newest.yml`): each server's latest release. It runs
  nightly beside the pinned round; on demand it's a checkbox.

While the newest round fails, one issue labelled `upstream` stays open with
the releases it tested, and closes itself once a run passes. A run whose
jobs couldn't pull their images opens none, since it tested nothing.

The jobs sign in to ghcr.io with the workflow's own token, and to Docker Hub
with `DOCKERHUB_PULL_TOKEN` (a read-only token, skipped when it isn't set),
because the runners' shared addresses get rate-limited when they pull
anonymously. A refused pull is retried up to three times.

## Plex

Plex needs a real plex.tv account to claim the server, so it uses an account
set aside for testing. Each run claims a new server under its own name, so
two runs can share the account. When the run ends, pass or fail, it removes
its own server and the AutoThrottle sign-ins it made, plus any test devices
over an hour old that an interrupted run left behind.

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
| `compose.yml` | The stack, with each server pinned to the oldest version the main README supports |
| `compose.newest.yml` | Each server at its newest release, for `E2E_NEWEST=1` |
| `provision.py` | Prepares the stack before it starts and sets up the media server after |
| `scenario.js` | The browser test |
| `run.sh` | Runs everything |
| `reset.sh` | Rebuilds the stack from nothing, pulling its images with retries |
| `pins_test.go` | Fails CI when a pin and the main README's supported versions disagree |
| `fixtures/` | The test video, a 200 KB, ten-minute H.264 file, filed as a film (`movies/`) and as both episodes of a test show (`tv/`), the second being its last |
