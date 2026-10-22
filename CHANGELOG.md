# Changelog

All notable changes to AutoThrottle are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/2.0.0/),
and AutoThrottle adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Signing in with Plex no longer leaves an empty gap under the button once it has signed in.
- A Plex app that keeps reporting a paused stream Plex has already dropped no longer switches throttling on and off every minute while nothing is playing.
- A media server already added under another address, such as its IP and its hostname, is now refused instead of having its streams counted twice.

## [1.1.0] - 2026-10-19

### Added

- **Deluge integration**: Deluge 2.1+ is now supported alongside qBittorrent and Transmission. ([#6])
- **Windows installer**: AutoThrottle now runs natively on Windows 10 and 11 (x64 and Arm) as a background service.
- **Unraid Community Apps**: Now available to [install](https://ca.unraid.net/apps/autothrottle-0xufrs017fz9b0) on Unraid as a community app.
- **Autoplay-aware throttling**: Back-to-back episodes being played will hold throttling, preventing unnecessary rate changes.

### Changed

- The address field now suggests the standard port for each client and server type.
- Following an expired or old link while signed out now redirects to the sign-in page instead of a 404 error.

### Fixed

- Sensitive credentials and tokens are only sent to the entered address, never to external redirects.
- The status beside the Dashboard title and its Disable/Resume button now update live.
- Editing a saved connection no longer requires a new test unless the address or credentials have changed.
- Connection errors wrap cleanly instead of splitting words and addresses.

### Dependencies

- Add fyne.io/systray v1.12.2 by [@JaiMistry](https://github.com/JaiMistry) ([dd1e5ca])
- Add golang.org/x/sys v0.48.0 by [@JaiMistry](https://github.com/JaiMistry) ([dd1e5ca])

## [1.0.1] - 2026-10-14

### Changed

- Load the sign-in page when a session expires instead of showing it inside the app
- Highlight a throttle stretch's hours on hover and show its whole time in the tooltip

### Dependencies

- Update Go to 1.27.2 by [@renovate[bot]](https://github.com/apps/renovate) ([#3])
- Update modernc.org/sqlite to v1.60.1 by [@renovate[bot]](https://github.com/apps/renovate) ([#3])
- Update source-map-js to 1.2.2 by [@JaiMistry](https://github.com/JaiMistry) ([4633477])

## [1.0.0] - 2026-10-10

Initial release.

[Unreleased]: https://github.com/JaiMistry/autothrottle/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/JaiMistry/autothrottle/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/JaiMistry/autothrottle/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/JaiMistry/autothrottle/releases/tag/v1.0.0
[#3]: https://github.com/JaiMistry/autothrottle/pull/3
[#6]: https://github.com/JaiMistry/autothrottle/issues/6
[4633477]: https://github.com/JaiMistry/autothrottle/commit/46334777035c276029e70d7496fa6d3c1675089f
[dd1e5ca]: https://github.com/JaiMistry/autothrottle/commit/dd1e5ca04c73f7b36edfbe009d32f92683d0917e
