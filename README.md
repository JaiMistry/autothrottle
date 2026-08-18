# AutoThrottle

Automatically throttles a torrent/download client's bandwidth while a
media server (Plex, Emby, or Jellyfin) is actively playing something,
then restores it when playback stops.

<!--
Badges will resolve once the repo goes public and the first image is
published:
[![Docker Pulls](https://img.shields.io/docker/pulls/jaimistry/autothrottle)](https://hub.docker.com/r/jaimistry/autothrottle)
[![Docker Image Size](https://img.shields.io/docker/image-size/jaimistry/autothrottle)](https://hub.docker.com/r/jaimistry/autothrottle)
[![codecov](https://codecov.io/gh/JaiMistry/autothrottle/branch/main/graph/badge.svg)](https://codecov.io/gh/JaiMistry/autothrottle)
-->

## Status

Early development — not yet ready to run for real.

## Development

```
docker compose up --build
```

Serves on `:8080`.
