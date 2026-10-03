<p align="center">
  <img src="internal/web/static/img/logo-mark.svg" width="96" height="96" alt="AutoThrottle logo">
</p>

<h1 align="center">AutoThrottle</h1>

<p align="center">
  <strong>Your downloads slow down the moment someone starts watching — and speed back up when they stop.</strong>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-GPL--3.0-blue" alt="License: GPL-3.0"></a>
  <img src="https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white" alt="Go 1.26">
  <img src="https://img.shields.io/badge/platform-linux%2Famd64%20%7C%20linux%2Farm64-lightgrey" alt="Platforms">
  <!-- These resolve once the repository is public and the first image is published:
  <a href="https://hub.docker.com/r/jaimistry/autothrottle"><img src="https://img.shields.io/docker/pulls/jaimistry/autothrottle" alt="Docker pulls"></a>
  <a href="https://hub.docker.com/r/jaimistry/autothrottle"><img src="https://img.shields.io/docker/image-size/jaimistry/autothrottle" alt="Image size"></a>
  <a href="https://github.com/JaiMistry/autothrottle/releases"><img src="https://img.shields.io/github/v/release/JaiMistry/autothrottle" alt="Latest release"></a>
  <a href="https://codecov.io/gh/JaiMistry/autothrottle"><img src="https://codecov.io/gh/JaiMistry/autothrottle/branch/main/graph/badge.svg" alt="Coverage"></a>
  -->
</p>

---

If you run a media server and a torrent client on the same connection, you
know the problem: someone presses play, the torrents are saturating your
upload, and the stream buffers. AutoThrottle watches **Plex**, **Emby** and
**Jellyfin** in real time and throttles **qBittorrent** and **Transmission**
for exactly as long as something is playing — then puts them back.

No schedules to maintain, no "slow mode" to remember to switch off.

<!-- Screenshots — add once the Dashboard redesign is final:
<p align="center">
  <img src="docs/screenshots/dashboard-dark.png" width="820" alt="Dashboard">
</p>
-->

## Features

- **Real-time, not polling.** Listens to each media server's live
  notification feed, so throttling starts within seconds of playback.
- **Many to many.** Map any media server to any download client. Two
  servers playing at once? Each downloader knows exactly which one is
  holding it back.
- **Handles real-world playback.** A pause keeps the throttle on — the
  viewer is coming back. A stream left paused, or a player that crashed
  mid-episode, is released after 30 minutes instead of throttling all night.
- **Local playback can be exempt.** Watching from the same network doesn't
  compete with your internet connection, so it needn't slow anything down.
- **Survives restarts.** Rates are re-asserted the moment AutoThrottle
  starts, so a crash mid-throttle never leaves a client stuck slow.
- **Live dashboard.** What is throttled, why, which sessions are playing and
  how far in — updating as it happens.
- **A full log.** Every throttle and restore, with the server that caused it,
  plus warnings when a server stops answering.
- **One small container.** A single Go binary with an embedded database.
  Typically uses 10–25 MB of memory.

## Quick start

1. **Create a folder** for AutoThrottle and a `data` directory inside it:

   ```sh
   mkdir -p autothrottle/data && cd autothrottle
   ```

2. **Save this as `docker-compose.yml`:**

   ```yaml
   services:
     autothrottle:
       image: jaimistry/autothrottle:latest
       container_name: autothrottle
       user: "${PUID:-1000}:${PGID:-1000}"
       environment:
         - TZ=America/Los_Angeles
       volumes:
         - ./data:/data
       ports:
         - "8080:8080"
       restart: unless-stopped
   ```

3. **Start it:**

   ```sh
   docker compose up -d
   ```

4. **Get your setup code** from the log:

   ```sh
   docker logs autothrottle
   ```

5. **Open `http://<your-server>:8080`**, enter the setup code, and create
   your account.

The image is also published as `ghcr.io/jaimistry/autothrottle`.

## First-time setup

Once you're signed in, everything happens on the **Configuration** page:

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
| `PUID` / `PGID` | `1000` / `1000` | The user and group the container runs as. Use the owner of your `data` directory — `id -u` and `id -g` on the host. These are read by Docker Compose for the `user:` line, so set them in your shell or a `.env` file next to the compose file, not under `environment:`. |
| `AUTH_USERNAME` | — | Sign in with these credentials instead of creating an account in the browser. Set both or neither. |
| `AUTH_PASSWORD` | — | |
| `AUTH_DISABLED` | `false` | Set to `true` to turn login off entirely — only if AutoThrottle sits behind a reverse proxy that does its own authentication. |
| `PORT` | `8080` | Port to listen on inside the container. |
| `DATA_DIR` | `/data` | Where the database and encryption key live. |

### Signing in

With no credentials set, the first visit asks you to create an account. To
make sure nobody else on your network can claim the instance before you, the
form asks for a **one-time setup code** that is only ever printed to the
container log. It stops working as soon as the account exists.

**Forgot your password?** Set `AUTH_USERNAME` and `AUTH_PASSWORD` and restart.
Environment credentials always take priority over the stored account.

### Behind a reverse proxy

AutoThrottle works as-is behind Traefik, Caddy, nginx and the like. It
detects HTTPS from the `X-Forwarded-Proto` header and secures its login
cookie automatically — there is nothing to configure.

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

Tested against real servers:

| | Version |
|---|---|
| Plex Media Server | 1.43 |
| Jellyfin | 10.11 |
| Emby | 4.9 |
| qBittorrent | 5.2 (the 4.x login is handled too) |
| Transmission | 4.1 |

## Troubleshooting

**The container exits saying the data directory is not usable.**
The container's user can't write to `./data`. Docker creates a missing
directory as root, so either `mkdir data` before the first start, or
`sudo chown 1000:1000 data` (matching your `PUID`/`PGID`).

**qBittorrent says "too many failed attempts".**
qBittorrent temporarily bans an address after repeated wrong passwords.
Wait for the ban to expire (an hour by default), or restart qBittorrent.

**A media server shows as connecting or unreachable.**
Check the address is reachable *from the container*: `localhost` inside
the container means the container itself, not your server. Use the host's
LAN address instead.

**Health checks.**
`GET /health` reports whether each media server and download client is
reachable, as JSON. It answers `200` when everything is, `503` when anything
isn't, and needs no login — so it plugs straight into an uptime monitor.

## Security

- Stored credentials (tokens, API keys, download-client passwords) are
  encrypted in the database with a key that is generated on first run and
  kept in `DATA_DIR`. Back up the whole directory, not just the database.
- Account passwords are stored salted and hashed (PBKDF2-SHA256).
- Logins and setup attempts are rate-limited, and every change made inside
  the app is protected against cross-site request forgery.

## Building from source

```sh
git clone https://github.com/JaiMistry/autothrottle.git
cd autothrottle
docker compose -f docker-compose.dev.yml up --build
```

## License

[GPL-3.0](LICENSE). You're free to use, study, share and modify AutoThrottle;
if you distribute a modified version, it must be released under the same
license.
