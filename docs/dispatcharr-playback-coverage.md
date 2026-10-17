# Dispatcharr status-based throttling

Dispatcharr's reported client status is the source of truth:

- At least one reported client: throttle the mapped downloader.
- Successful status response with no reported clients: remove the playback throttle.
- Failed request or incomplete status: keep the last known state until a valid response arrives.

AutoThrottle reads live, VOD and catch-up status together from `/proxy/stats/` every two seconds. The decision does not depend on the player, stream format or playback route. Active recording jobs count through their reported live clients. Pause and position metadata are for display; a reported paused client still counts.

The optional local-playback exemption is off by default. If enabled, it filters local clients before the throttle decision; unknown addresses still count. Live client summaries are expanded when truncated, so clients beyond the first ten are included. Buffers without clients do not count. Idle VOD sessions count for as long as Dispatcharr reports them.

## Checks completed on Unraid

The tests ran against Dispatcharr 0.32.0 on 18 October 2026. The changed watcher requested the real authenticated status endpoint on the Unraid host. The coverage probes made no production downloader writes or changes to existing stream profiles or channels.

| Check | Evidence |
| --- | --- |
| Client appears and disappears | Real live playback verified first-client throttling and release through DockerMan-managed AutoThrottle and qBittorrent services with fixed IPs. |
| Reported status is read correctly | Real live, VOD, catch-up and recording clients were observed. The changed watcher returned matching sessions for actual VOD and catch-up HTTP playback. |
| Paused clients still count | A native catch-up pause report at 125 seconds remained present in the watcher's output with fixed position. |
| qBittorrent time-dependent limits | An isolated qBittorrent verified actual scheduled entry/exit during playback, forced restart and restoration of normal and alternative rate sets. |
| Automated checks | Full Go tests, changed-client race checks and `go vet` passed on Unraid. Earlier scheduler/web race checks also passed. |

The VOD/catch-up probes checked the actual watcher's session output. Direct downloader throttle/release was exercised in the live-playback tests. The status rule is shared across reported clients; it does not require a separate format-specific detection mechanism.

Redirect and finished-recording-file probes confirmed that those requests did not appear as clients in Dispatcharr's status. They therefore did not create a throttle-relevant session, matching the agreed scope.

The qBittorrent changes and a concrete rate example are explained in [qBittorrent speed schedules](../README.md#qbittorrent-speed-schedules). Production rate banks and schedule settings were checked against their saved baseline after the coverage probes and were unchanged. All task containers and temporary server data were removed.

The API schema was verified against the deployed source and Dispatcharr's [combined status endpoint](https://github.com/Dispatcharr/Dispatcharr/blob/main/apps/proxy/stats_views.py).
