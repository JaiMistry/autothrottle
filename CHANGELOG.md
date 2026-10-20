# Changelog

All notable changes to AutoThrottle are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and AutoThrottle adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Signing in with Plex no longer leaves an empty gap under the button once it has signed in.

## [1.1.0] - 2026-10-19

### Added

- **Deluge integration**: Deluge 2.1+ is now supported alongside qBittorrent and Transmission. #6
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

- Add fyne.io/systray v1.12.2 by @JaiMistry in dd1e5ca
- Add golang.org/x/sys v0.48.0 by @JaiMistry in dd1e5ca

## [1.0.1] - 2026-10-14

### Changed

- Load the sign-in page when a session expires instead of showing it inside the app
- Highlight a throttle stretch's hours on hover and show its whole time in the tooltip

### Dependencies

- Update Go to 1.27.2 by @renovate[bot] in #3
- Update modernc.org/sqlite to v1.60.1 by @renovate[bot] in #3
- Update source-map-js to 1.2.2 by @JaiMistry in 4633477

## [1.0.0] - 2026-10-10

Initial release.

[Unreleased]: https://github.com/JaiMistry/autothrottle/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/JaiMistry/autothrottle/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/JaiMistry/autothrottle/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/JaiMistry/autothrottle/releases/tag/v1.0.0
