<p align="center">
  <img src="internal/web/static/img/logo-mark.svg" width="96" height="96" alt="AutoThrottle logo">
</p>

<h1 align="center">AutoThrottle</h1>

<p align="center">
  <strong>Your torrents back off the moment someone hits play.</strong>
</p>

<p align="center">
  <a href="https://hub.docker.com/r/jaimistry/autothrottle"><img src="https://img.shields.io/docker/pulls/jaimistry/autothrottle" alt="Docker pulls"></a>
  <a href="https://hub.docker.com/r/jaimistry/autothrottle"><img src="https://img.shields.io/docker/image-size/jaimistry/autothrottle/latest" alt="Image size"></a>
  <a href="https://github.com/JaiMistry/autothrottle/releases"><img src="https://img.shields.io/github/downloads/JaiMistry/autothrottle/total" alt="Downloads"></a>
  <a href="https://codecov.io/gh/JaiMistry/autothrottle"><img src="https://codecov.io/gh/JaiMistry/autothrottle/branch/main/graph/badge.svg" alt="Coverage"></a>
</p>

---

Torrents and streaming fighting over the same connection means buffering.
AutoThrottle watches **Plex**, **Emby** and **Jellyfin**, slows
**qBittorrent** and **Transmission** the instant playback starts, and puts
them back to full speed when it stops.

<!-- Screenshots — add once the Dashboard redesign is final:
<p align="center">
  <img src="docs/screenshots/dashboard-dark.png" width="820" alt="Dashboard">
</p>
-->

## Features

- **Real-time** — reacts within seconds, from each server's live feed.
- **Many to many** — any media server can throttle any download client.
- **Pause-aware** — pauses stay throttled; abandoned streams are released.
- **Local playback exemption** — streams on your own network can be ignored.
- **Restart-safe** — rates are re-applied on startup, so nothing stays stuck slow.
- **Live dashboard** — what's throttled, why, and what's playing.
- **Full log** — every throttle and restore, with its cause.
- **Lightweight** — one Go binary, embedded database, ~20 MB of memory.

## Quick start

```yaml
services:
  autothrottle:
    image: jaimistry/autothrottle:latest
    container_name: autothrottle
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=America/Los_Angeles
    volumes:
      - ./data:/data
    ports:
      - "7676:7676"
    restart: unless-stopped
```

Or with `docker run`:

```sh
docker run -d \
  --name autothrottle \
  -e PUID=1000 \
  -e PGID=1000 \
  -e TZ=America/Los_Angeles \
  -v "$(pwd)/data:/data" \
  -p 7676:7676 \
  --restart unless-stopped \
  jaimistry/autothrottle:latest
```

## First-time setup

Open `http://<your-server>:7676` and create your account. Everything else
happens on the **Configuration** page:

1. **Add a media server** — Plex, Emby or Jellyfin, with its URL and token or
   API key. For Plex you can sign in with your Plex account instead of
   pasting a token.
2. **Add a download client** — qBittorrent or Transmission, with two pairs of
   rates:
   - **Default rates** — applied when nothing is playing.
   - **Throttled rates** — applied while a mapped server is playing.

   Rates are in KB/s; `0` means unlimited.
3. **Map them.** Choose which media servers should throttle which download
   clients.

Every connection must pass **Test connection** before it can be saved, and
the test checks your credentials, not just that the address answers.

## Configuration

AutoThrottle needs no environment variables at all. These are available if
you want them:

| Variable | Default | What it does |
|---|---|---|
| `TZ` | `America/Los_Angeles` | Time zone for every timestamp in the app and its log. Any [tz database name](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones), e.g. `Europe/London`. |
| `PUID` / `PGID` | `1000` / `1000` | The user and group AutoThrottle runs as, and that will own its data folder. Use your own — run `id -u` and `id -g` on the host. Ownership of the folder is fixed automatically on start. |
| `AUTH_USERNAME` | — | Sign in with these credentials instead of creating an account in the browser. Set both or neither. |
| `AUTH_PASSWORD` | — | |
| `AUTH_DISABLED` | `false` | Set to `true` to turn login off entirely — only if AutoThrottle sits behind a reverse proxy that does its own authentication. |

## How throttling decides

| Situation | Result |
|---|---|
| Something is playing on a mapped server | Throttled |
| Playback is paused | Still throttled — the viewer is coming back |
| Paused for more than 30 minutes | Released, until playback resumes |
| A session stops advancing (crashed or abandoned player) | Released after 30 minutes |
| Playback from the local network, with exemption on | Ignored |
| Nothing mapped is playing | Default rates |
| **Disable throttling** pressed on the Dashboard | Default rates everywhere until resumed |

## Supported versions

| Media server | Version |
|---|---|
| Plex Media Server | 1.43+ |
| Jellyfin | 10.11+ |
| Emby | 4.9+ |

| Download client | Version |
|---|---|
| qBittorrent | 5.2+ |
| Transmission | 4.1+ |

## Health check

`GET /health` reports whether each media server and download client is
reachable, as JSON: `200` when everything is, `503` when anything isn't. It
needs no login, so it plugs straight into an uptime monitor.

## License

[GPL-3.0](LICENSE)
