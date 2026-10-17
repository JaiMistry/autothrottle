<p align="center">
  <img src="internal/web/static/img/logo-mark.svg" width="160" height="160" alt="AutoThrottle logo">
</p>

<h1 align="center">AutoThrottle</h1>

<p align="center">
  <strong>Intelligently throttles download clients while a media server has active playback.</strong>
</p>

<p align="center">
  <a href="https://github.com/JaiMistry/autothrottle/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/JaiMistry/autothrottle/ci.yml?branch=main&label=build" alt="Build status"></a>
  <a href="https://codecov.io/gh/JaiMistry/autothrottle"><img src="https://codecov.io/gh/JaiMistry/autothrottle/branch/main/graph/badge.svg" alt="Coverage"></a>
  <a href="https://github.com/JaiMistry/autothrottle/releases/latest"><img src="https://img.shields.io/docker/v/jaimistry/autothrottle?sort=semver&label=version" alt="Latest version"></a>
  <a href="https://hub.docker.com/r/jaimistry/autothrottle"><img src="https://img.shields.io/docker/pulls/jaimistry/autothrottle" alt="Docker pulls"></a>
  <a href="https://hub.docker.com/r/jaimistry/autothrottle"><img src="https://img.shields.io/docker/image-size/jaimistry/autothrottle/latest?label=size" alt="Size"></a>
  <a href="https://ca.unraid.net/apps/autothrottle-0xufrs017fz9b0"><img src="https://img.shields.io/badge/Unraid-Community%20Apps-f15a2c" alt="Unraid Community Apps"></a>
</p>

AutoThrottle dynamically lowers your download client speeds while something is playing on your media server, and restores them when playback stops. Set it up once and it looks after itself from then on. The throttling is aware of what is actually happening. A paused stream stays throttled, one that has sat paused or stalled for 30 minutes is let go, and playback on your own network can be ignored. When you want full speed regardless, one click turns throttling off until you turn it back on.

<p align="center">
  <img src=".github/assets/screenshots/dashboard-dark-light.png" width="100%" alt="The Dashboard in dark and light themes">
</p>
<p align="center">
  <img src=".github/assets/screenshots/connections-light.png" width="49%" alt="Connections">
  <img src=".github/assets/screenshots/login-dark.png" width="49%" alt="Sign-in">
</p>

## ⭐ Features

- **Live dashboard:** a reactive modern UI.
- **Real-time:** detects within seconds, from each server's live feed.
- **Mappings:** control which applications are throttled.
- **Pause-aware:** a paused stream stays throttled, and an abandoned one is released.
- **Local playback exemption:** streams on your own network can be ignored.
- **Restart-safe:** rates are re-applied on startup, so nothing stays stuck slow.
- **Throttle history:** a heatmap of when your downloads were held back.
- **Secure:** all credentials securely encrypted.
- **Lightweight:** very small footprint.

## 🧩 Supported versions

| | Media server | Version |
|:-:|---|---|
| <a href="https://www.plex.tv"><img src=".github/assets/logos/plex.png" width="18" alt="Plex Media Server"></a> | Plex Media Server | 1.43+ |
| <a href="https://jellyfin.org"><img src=".github/assets/logos/jellyfin.png" width="18" alt="Jellyfin"></a> | Jellyfin | 10.11+ |
| <a href="https://emby.media"><img src=".github/assets/logos/emby.png" width="18" alt="Emby"></a> | Emby | 4.9+ |
| | [Dispatcharr](https://github.com/Dispatcharr/Dispatcharr) | 0.32+ |

| | Download client | Version |
|:-:|---|---|
| <a href="https://www.qbittorrent.org"><img src=".github/assets/logos/qbittorrent.svg" width="18" alt="qBittorrent"></a> | qBittorrent | 5.2+ |
| <a href="https://transmissionbt.com"><img src=".github/assets/logos/transmission.png" width="18" alt="Transmission"></a> | Transmission | 4.1+ |

### Dispatcharr

Add a media server with type **Dispatcharr**, its base URL, and an administrator API key. Map it to a download client as usual. AutoThrottle checks the combined `/proxy/stats/` endpoint every two seconds, authenticating with `X-API-Key`. The connection test requires valid live, VOD and catch-up sections; an expiring JWT is not needed.

Each reported live, VOD or catch-up client counts as playback, including clients making a recording. A live channel buffer with no clients does not count. **Ignore local network playback** uses each client's reported IP address; unknown addresses still count. Reverse proxies must preserve the real client IP for that exemption to be useful. Live channel detail is requested when the summary omits clients beyond its ten-client limit.

VOD position/duration and catch-up pause/position are displayed when Dispatcharr reports them. A paused catch-up session still counts. Other players may buffer or pause without reporting that state; a reported connection continues to count. Dispatcharr can retain an idle VOD session for reconnection, so release follows the server's session lifecycle. If polling fails or a required status section is missing, the last known sessions are kept until a successful response reconciles them.

**Coverage is incomplete; the integration PR is held closed.** Live tests confirmed that provider redirects and playback of saved DVR files can transfer video while all three Dispatcharr statistics sections report zero clients. Complete playback coverage requires additional session reporting from Dispatcharr and, for provider redirects, a way to observe playback after the hand-off. These are unresolved requirements, not excluded features. See the [playback coverage report](docs/dispatcharr-playback-coverage.md).

### qBittorrent speed schedules

When qBittorrent's speed scheduler is enabled, AutoThrottle follows its own normal and alternative rates instead of applying the configured Default rates. While playback is active, both rate sets are temporarily capped at the configured Throttled rates. An existing stricter limit stays stricter. Schedule times, weekdays and the selected speed mode are never changed, so a time boundary during playback works without reproducing qBittorrent's clock or time zone logic.

The original rate sets are saved before the first change and survive an AutoThrottle restart. A failed media-server snapshot at startup cannot release a pending throttle; recovery waits for an authoritative response. Once playback ends, AutoThrottle restores each rate it still owns; external rate edits are preserved. Recovery also runs on graceful shutdown and before disabling, deleting or moving the download-client connection. Without qBittorrent scheduling, the existing fixed Default/Throttled rate policy is unchanged.

## 🚀 Quick start

Docker compose:

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
      - /data:/data
    ports:
      - "7676:7676"
    restart: unless-stopped
```

Docker run:

```sh
docker run -d \
  --name autothrottle \
  -e PUID=1000 \
  -e PGID=1000 \
  -e TZ=America/Los_Angeles \
  -v /data:/data \
  -p 7676:7676 \
  --restart unless-stopped \
  jaimistry/autothrottle:latest
```

Windows:

Download and run `AutoThrottle-Setup-<version>.exe` from the [latest release](https://github.com/JaiMistry/autothrottle/releases/latest).

## ⚙️ Environment variables

All are optional.

| Variable | Default | What it does |
|---|---|---|
| `TZ` | `America/Los_Angeles` | Time zone for timestamps. |
| `PUID` | `1000` | User that owns the data folder. |
| `PGID` | `1000` | Group that owns the data folder. |
| `AUTH_USERNAME` | | Sign-in username from the environment instead of creating an account. Set with `AUTH_PASSWORD`. |
| `AUTH_PASSWORD` | | Sign-in password from the environment. Set with `AUTH_USERNAME`. |
| `AUTH_DISABLED` | `false` | Turns off sign-in. Only use this behind a proxy that handles it. |

## 🛠️ First-time setup

Open `http://<your-server>:7676` and create your account. Everything else
happens on the **Configuration** page:

1. **Add a media server** with its URL and token or API key. For Plex you can
   sign in with your Plex account instead of pasting a token.
2. **Add a download client** with two pairs of rates:
   - **Default rates**, applied when nothing is playing.
   - **Throttled rates**, applied while a mapped server is playing.

   Rates are in KB/s, and `0` means unlimited.
3. **Map them.** Choose which media servers throttle which download clients.

## License

[GPL-3.0](LICENSE)

<p align="center"><sub>Made with ❤️ by <a href="https://github.com/JaiMistry">JaiMistry</a></sub></p>
