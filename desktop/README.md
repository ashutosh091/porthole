# Porthole desktop

A menu-bar (tray) app and window for Porthole, built with Go and [Wails v3](https://v3.wails.io). It reads and writes the same `~/.config/porthole/profiles.toml` as the `porthole` CLI. Installers for macOS, Windows and Linux are on the [Releases](https://github.com/ashutosh091/porthole/releases/latest) page.

It runs tunnels itself, with no Docker:
- **WireGuard profiles** (`wg_conf = "secrets/x.conf"`) run in-process: userspace wireguard-go plus the gVisor netstack. No admin rights, no system VPN, no routing changes.
- **SSH profiles** run the system `ssh -D`, so `~/.ssh/config` aliases, keys and agents work.
- Every profile gets one local port that speaks both SOCKS5 and HTTP. Chrome, Brave and Firefox use SOCKS5. Safari mode uses HTTP through a PAC, so DNS resolves at the far end either way.
- Profiles that pick servers from gluetun's built-in country list (`countries = ...`, no `wg_conf`) still need the CLI; the app lists them as CLI-only.

## Use

- **Import .conf** picks downloaded WireGuard configs (from your VPN provider or your own server) and copies them into `~/.config/porthole/secrets/`. Names like `uk-lon.conf` or `us-nyc.conf` fill in the country (GB, US) and timezone.
- **Add server** adds an SSH exit: a host or `~/.ssh/config` alias, an optional user and key.
- Each profile has a button per installed browser (Chrome, Brave, Firefox, and Safari on macOS). Each opens that browser through the profile, connecting first if needed, after checking the exit country against `expect_country`.
- **Safari** (macOS) sets a system PAC for the profile's `pac_set` domains and restarts Safari. The previous proxy settings come back when Safari quits, when you press Stop, or when the app quits. The ⋯ menu has **Safari all traffic** to route everything.
- The ⋯ menu also has **Re-check**, **Test for leaks** (IP, WebRTC and DNS leak pages through the profile), **Edit** and **Delete**. Renaming a profile moves its browser data, so logins survive.
- The menu-bar icon holds the same actions. Closing the window keeps tunnels running; **Quit Porthole** stops them.

## Platform notes

- **macOS:** everything, including Safari mode.
- **Windows:** Chrome, Brave and Firefox. The per-profile timezone isn't applied yet (Chromium ignores `TZ` on Windows), and the "profile already open through another region" check is skipped.
- **Linux:** Chrome/Chromium, Brave and Firefox. Needs GTK 4 and WebKitGTK 6.0 (`libgtk-4-1`, `libwebkitgtk-6.0-4`) and, for the tray icon, a desktop with StatusNotifier/AppIndicator support.

## Build

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
cd desktop
wails3 package            # macOS: bin/porthole-desktop.app (ad-hoc signed); Windows: NSIS installer; Linux: AppImage, deb, rpm
wails3 dev                # live-reload development
go test ./internal/core   # offline tests; PORTHOLE_LIVE=uk,us go test -run Live ./internal/core hits real exits
```

Releases are built by [`.github/workflows/release.yml`](../.github/workflows/release.yml) when a `v*` tag is pushed.
