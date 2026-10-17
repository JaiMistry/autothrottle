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

Add a media server with type **Dispatcharr**, its base URL, and an administrator API key. Map it to a download client as usual. AutoThrottle polls Dispatcharr's combined `/proxy/stats/` endpoint every two seconds using the `X-API-Key` header.

The rule is based on Dispatcharr's reported clients: **one or more clients means throttle; a successful response with no clients means release the throttle.** Live, VOD and catch-up status are read together. The player, stream format and playback route do not affect this decision. Active recordings count through their reported live-stream clients. A paused session counts as long as Dispatcharr still reports it.

**Ignore local network playback** remains optional and is off by default. When enabled, it filters clients by their reported IP address; unknown addresses still count. A live buffer without clients does not count. If a required status section is missing or a request fails, AutoThrottle keeps its last known state. Release follows Dispatcharr's session lifecycle, including any idle VOD sessions it retains. See the [status and test report](docs/dispatcharr-playback-coverage.md).

### qBittorrent speed schedules

Previously, AutoThrottle applied its configured fixed Default/Throttled rates. That could overwrite qBittorrent's own time-dependent limits. When qBittorrent's speed scheduler is enabled, AutoThrottle now uses qBittorrent's existing normal and alternative limits as the baseline instead of its configured Default rates.

While a reported media client exists, both rate sets are temporarily capped at the configured Throttled rates, for download and upload. A stricter existing limit stays stricter. For example, with normal download at 20,000 KiB/s, alternative download at 3,000 KiB/s and a throttle cap of 5,000 KiB/s, the temporary limits are 5,000 and 3,000 KiB/s. qBittorrent still decides when to switch between them: schedule times, weekdays and mode selection are unchanged.

When the last client disappears, the original normal and alternative limits are restored, leaving the currently applicable rate to qBittorrent's schedule. The originals are saved before any change and survive an AutoThrottle restart. External limit edits are preserved. Recovery also runs on graceful shutdown and before disabling, deleting or moving a download-client connection; an unavailable media-server status at startup cannot release a recovered throttle prematurely. When qBittorrent scheduling is disabled, the existing fixed Default/Throttled behavior is unchanged.

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
