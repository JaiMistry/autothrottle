<p align="center">
  <img src="internal/web/static/img/logo-mark.svg" width="160" height="160" alt="AutoThrottle logo">
</p>

<h1 align="center">AutoThrottle</h1>

<p align="center">
  <strong>Intelligently throttles download clients while a media server has active playback.</strong>
</p>

<p align="center">
  <a href="https://github.com/JaiMistry/autothrottle/releases/latest"><img src="https://img.shields.io/github/v/release/JaiMistry/autothrottle?label=version" alt="Latest version"></a>
  <a href="https://github.com/JaiMistry/autothrottle/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/JaiMistry/autothrottle/ci.yml?branch=main&label=build" alt="Build status"></a>
  <a href="https://codecov.io/gh/JaiMistry/autothrottle"><img src="https://codecov.io/gh/JaiMistry/autothrottle/branch/main/graph/badge.svg" alt="Coverage"></a>
  <a href="https://hub.docker.com/r/jaimistry/autothrottle"><img src="https://img.shields.io/docker/pulls/jaimistry/autothrottle" alt="Docker pulls"></a>
  <a href="https://hub.docker.com/r/jaimistry/autothrottle"><img src="https://img.shields.io/docker/image-size/jaimistry/autothrottle/latest?label=size" alt="Size"></a>
  <a href="https://github.com/JaiMistry/autothrottle/releases"><img src="https://img.shields.io/github/downloads/JaiMistry/autothrottle/total" alt="Downloads"></a>
</p>

AutoThrottle dynamically lowers your download client speeds while something is playing on your media server, and restores them when playback stops. Set it up once and it looks after itself from then on. The throttling is aware of what is actually happening. A paused stream stays throttled, one that has sat paused or stalled for 30 minutes is let go, and playback on your own network can be ignored. When you want full speed regardless, one click turns throttling off until you turn it back on.

<!-- Screenshots: add once the Dashboard redesign is final.
<p align="center">
  <img src="docs/screenshots/dashboard-dark.png" width="820" alt="Dashboard">
</p>
-->

## ⭐ Features

- **Real-time:** reacts within seconds, from each server's live feed.
- **Mappings:** control which applications are throttled.
- **Pause-aware:** a paused stream stays throttled, and an abandoned one is released.
- **Local playback exemption:** streams on your own network can be ignored.
- **Restart-safe:** rates are re-applied on startup, so nothing stays stuck slow.
- **Live dashboard:** what's throttled, why, and what's playing.
- **Throttle history:** a heatmap of when your downloads were held back.
- **Full log:** every throttle and restore, with its cause.
- **Lightweight:** one small container with nothing else to run beside it.

## 🧩 Supported versions

| | Media server | Version |
|:-:|---|---|
| <a href="https://www.plex.tv"><img src=".github/assets/logos/plex.png" width="18" alt="Plex Media Server"></a> | Plex Media Server | 1.43+ |
| <a href="https://jellyfin.org"><img src=".github/assets/logos/jellyfin.png" width="18" alt="Jellyfin"></a> | Jellyfin | 10.11+ |
| <a href="https://emby.media"><img src=".github/assets/logos/emby.png" width="18" alt="Emby"></a> | Emby | 4.9+ |

| | Download client | Version |
|:-:|---|---|
| <a href="https://www.qbittorrent.org"><img src=".github/assets/logos/qbittorrent.svg" width="18" alt="qBittorrent"></a> | qBittorrent | 5.2+ |
| <a href="https://transmissionbt.com"><img src=".github/assets/logos/transmission.png" width="18" alt="Transmission"></a> | Transmission | 4.1+ |

## 🚀 Quick start

`docker compose`:

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

`docker run`:

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

The image is also published as `ghcr.io/jaimistry/autothrottle`, and attached
to each [release](https://github.com/JaiMistry/autothrottle/releases) for
loading with `docker load` where there is no registry.

## ⚙️ Environment variables

All are optional.

| Variable | Default | What it does |
|---|---|---|
| `TZ` | `America/Los_Angeles` | Time zone for timestamps. |
| `PUID` / `PGID` | `1000` / `1000` | User and group that own the data folder. |
| `AUTH_USERNAME` / `AUTH_PASSWORD` | | Sign-in from the environment instead of creating an account. Set both or neither. |
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

---

<p align="center"><sub>Made with ❤️ by <a href="https://github.com/JaiMistry">JaiMistry</a></sub></p>
