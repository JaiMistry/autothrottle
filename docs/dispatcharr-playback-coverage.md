# Dispatcharr playback coverage

The requirement is to throttle whenever any Dispatcharr video is playing, starting with one viewer. The integration remains incomplete and its PR remains a draft until that requirement can be verified. Missing playback paths are blockers rather than accepted scope exclusions.

## Verification on Unraid

The following probes ran against the deployed Dispatcharr 0.32.0 instance on 18 October 2026. The new Go watcher ran on the same Unraid host and requested the real authenticated `/proxy/stats/` endpoint. No production download-client limits, schedules, stream profiles or existing channels were changed during these coverage probes.

| Playback path | Evidence | Result |
| --- | --- | --- |
| Live TS proxy | Previously tested actual video, first-viewer throttling, restart/recovery and release through DockerMan-managed AutoThrottle and qBittorrent test services. The combined response contains the same live channel/client schema. | Live TS behavior verified; new parser also has regression coverage. |
| Movie VOD proxy | Real HTTP 200 video transfer; the new Go watcher returned the matching VOD session from the actual API. | Detected. This probe checked session detection without writing downloader settings. |
| Catch-up proxy | Real HTTP 200 archived video transfer; the new Go watcher returned the matching catch-up session. | Detected. This probe checked session detection without writing downloader settings. |
| Catch-up pause/position | The native position endpoint accepted `position_secs: 125, paused: true` with HTTP 204. The new watcher still returned the session with paused state and a fixed 125-second position. | Reporting verified; pause does not remove the throttle-relevant session. |
| Direct live redirect | Dispatcharr returned HTTP 302. Following its target with the provider's configured user agent returned HTTP 200 and transferred 2,654,208 bytes over 12.08 seconds. All 14 combined-status samples reported zero live, VOD and catch-up clients. | **Blocker: active video is absent from the status API.** |
| Saved DVR file playback | Dispatcharr returned HTTP 206 and transferred 2,654,208 bytes over 8.06 seconds. Both combined-status samples reported zero live, VOD and catch-up clients. | **Blocker: active video is absent from the status API.** |
| Provider redirects for VOD/catch-up | Source inspection shows provider hand-off paths that bypass proxy session creation. Global production profiles were left unchanged. | Not live-confirmed; the same missing-observation problem must be resolved and tested. |
| Other entry points and output formats | XC routes, episode VOD, fMP4 output and DVR HLS need final end-to-end checks after the session-reporting blockers are resolved. | Complete coverage is not yet confirmed. |

The failed initial redirect attempts returned provider HTTP 403 and were not counted as successful playback. The successful test used the account's configured user agent. An initial catch-up candidate returned a provider error; a second channel delivered real archived video successfully.

## Required behavior

- Poll live, VOD and catch-up status together. Never silently fall back to live-only status when a required section is missing.
- Keep reported clients active even while paused. Use available position metadata for display without treating missing player reports as a playback stop.
- Preserve the existing local-playback option, conservative handling of unknown IPs, full live client detail beyond the summary's ten-client cap, and outage/restart recovery.
- Count saved-recording playback independently of the recording job that originally fetched its live channel.
- Obtain trustworthy start, ongoing-playback and stop information for provider redirects. A 302 request only proves a hand-off; it cannot prove how long the player remains connected to the provider. A fixed timeout would be a guess and would not meet the requirement.
- Re-run end-to-end first-viewer throttle/release tests for every supported playback path before marking the PR ready for review.

## Dispatcharr evidence

- [`apps/proxy/stats_views.py`](https://github.com/Dispatcharr/Dispatcharr/blob/main/apps/proxy/stats_views.py) combines live, VOD and catch-up statistics.
- [`apps/proxy/live_proxy/views.py`](https://github.com/Dispatcharr/Dispatcharr/blob/main/apps/proxy/live_proxy/views.py), `_handle_redirect_stream`, returns a redirect and releases the reserved stream slot before the player follows it.
- [`apps/proxy/vod_proxy/views.py`](https://github.com/Dispatcharr/Dispatcharr/blob/main/apps/proxy/vod_proxy/views.py) contains separate provider-redirect and persistent-proxy-session paths.
- [`apps/timeshift/views.py`](https://github.com/Dispatcharr/Dispatcharr/blob/main/apps/timeshift/views.py) contains provider hand-off and proxied catch-up paths; [`apps/timeshift/stats.py`](https://github.com/Dispatcharr/Dispatcharr/blob/main/apps/timeshift/stats.py) exposes native pause/position metadata.
- [`apps/channels/api_views.py`](https://github.com/Dispatcharr/Dispatcharr/blob/main/apps/channels/api_views.py), `RecordingViewSet.file`, serves a saved file without registering a live/VOD/catch-up client.

Additional upstream reporting is needed for DVR playback. Redirect playback also requires feedback from the player/provider or routing that keeps the stream observable. AutoThrottle cannot reconstruct that missing state from the current three statistics sections.
