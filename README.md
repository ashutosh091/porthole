# Porthole

**Open a browser window through another country, without putting your whole computer on a VPN.**

Each Porthole profile is a local proxy port that exits through one place: a server of yours over SSH, a WireGuard config from any VPN provider, or your own WireGuard server. Porthole opens Chrome, Brave or Firefox pointed at that port, with its own data folder, timezone and leak protection. The rest of the machine (other apps, other browser windows, your terminal) keeps its normal network. Several regions can be open side by side.

Website: https://ashutosh091.github.io/porthole

## What people use it for

- **Streaming and music catalogues** that differ by country, including Safari-only HD playback (see [Safari](#safari)).
- **Regional prices**: flights, hotels, software and shops that quote differently by visitor country.
- **Localised search and ads**: check how a site ranks, what ads run or how a page renders in another market.
- **Testing your own product**: geo-redirects, currency, language, consent banners and CDN behaviour per country.
- **Back-home sites while travelling**: banks, tax and government portals that block foreign IPs, through a server at home.
- **IP-allowlisted work tools**: reach dashboards that only accept the office IP through an office SSH host, without a full company VPN.
- **Keeping identities apart**: each region (or group of regions) has its own logins and cookies.

Respect the terms of the sites and services you use; some forbid access from outside a region.

## Two ways to run it

| | Desktop app | CLI |
|---|---|---|
| Platforms | macOS, Windows, Linux | macOS |
| WireGuard `.conf` profiles | in-process, no Docker or admin rights | a [gluetun](https://github.com/qdm12/gluetun) container (Docker) |
| SSH profiles | system `ssh -D` | system `ssh -D` |
| VPN provider by country (gluetun's server list) | no | yes |
| Safari mode | yes (macOS) | yes |

Both read the same `~/.config/porthole/profiles.toml`, so you can switch freely. The desktop app is in [`desktop/`](desktop/README.md).

## Install

**Desktop app:** download the installer for your OS from [Releases](https://github.com/ashutosh091/porthole/releases/latest):
- macOS (Apple silicon and Intel): [`Porthole-macos.dmg`](https://github.com/ashutosh091/porthole/releases/latest/download/Porthole-macos.dmg), then drag Porthole to Applications. The current build isn't notarized yet, so macOS blocks the first open: choose **System Settings → Privacy & Security → Open Anyway**.
- Windows: [`Porthole-windows-amd64-setup.exe`](https://github.com/ashutosh091/porthole/releases/latest/download/Porthole-windows-amd64-setup.exe).
- Linux: [`Porthole-linux-amd64.AppImage`](https://github.com/ashutosh091/porthole/releases/latest/download/Porthole-linux-amd64.AppImage), or the `.deb` / `.rpm` from the release page. Needs GTK 4 and WebKitGTK 6.0 (Ubuntu 24.04+, Fedora 39+ or similar).

**CLI (macOS, Python 3.11+, no packages to install):**

```bash
mkdir -p ~/.local/bin      # make sure it is on your PATH
curl -fsSLo ~/.local/bin/porthole https://github.com/ashutosh091/porthole/releases/latest/download/porthole
chmod +x ~/.local/bin/porthole
mkdir -p ~/.config/porthole/secrets
curl -fsSLo ~/.config/porthole/profiles.toml https://raw.githubusercontent.com/ashutosh091/porthole/main/profiles.example.toml
```

Then edit `~/.config/porthole/profiles.toml`. Profiles that use gluetun need Docker Desktop running.

## Profiles

[`profiles.example.toml`](profiles.example.toml) has a commented example of each kind:

| Kind | Config | How it tunnels |
|---|---|---|
| Your server over SSH | `type = "ssh"`, `host = "<alias or host>"` | `ssh -N -D 127.0.0.1:<port>`; nothing installed on the server |
| WireGuard `.conf` | `type = "gluetun"`, `provider = "custom"`, `wg_conf = "secrets/<file>.conf"` | desktop: userspace WireGuard; CLI: gluetun |
| VPN provider by country | `type = "gluetun"`, `provider = "<name>"`, `countries = "..."`, `env_file` | gluetun's built-in server list (CLI only) |

- **WireGuard configs:** most providers (IVPN, Mullvad, NordVPN, Proton VPN, Surfshark, Windscribe, …) offer per-location WireGuard configs under "manual setup". Save each one in `~/.config/porthole/secrets/` (`chmod 600`). In the desktop app, **Import .conf** does this for you, and names like `uk-lon.conf` or `us-nyc.conf` fill in the country and timezone. On every connect, Porthole reads the keys and endpoint and looks up the endpoint's current IP.
- **Prefer a `.conf` over `countries = ...`:** gluetun's built-in server lists can go stale, so a by-country profile may never connect.
- **SSH servers:** use a `~/.ssh/config` alias or a host plus `ssh_args`. The tunnel runs with `BatchMode`, so the key must load without a prompt (ssh-agent or an unencrypted key).
- **`expect_country`:** before opening the browser, Porthole checks the exit on ipinfo.io and refuses to open it if the country differs. Some VPN locations are *virtual* (the IP is registered in one country, the server sits in another), and geo-IP databases disagree about them; when ipinfo.io disagrees, Porthole asks ip-api.com too and, if that matches, warns and opens anyway.
- **`tz` and `lang`** set the browser's timezone and language so they match the exit.

## Use the CLI

```bash
porthole list                         # profiles and where they exit
porthole status                       # which tunnels are up, exit IP and country
porthole launch us                    # Chrome through "us" (starts the tunnel if needed)
porthole launch uk -b firefox https://example.com
porthole launch uk -b safari          # Safari, streaming list only (see below)
porthole launch uk -b safari --sites travel
porthole check us                     # show the exit again
porthole down us  |  porthole down --all
```

## Leak protection

- Chrome and Brave get `--proxy-server=socks5://…`, so DNS resolves at the far end, plus `--force-webrtc-ip-handling-policy=disable_non_proxied_udp` so WebRTC can't reveal your real IP.
- Firefox gets a `user.js` with remote DNS, WebRTC limited to the proxy, and DNS-over-HTTPS off.
- If a tunnel dies, pages fail to load; they don't fall back to your real IP.
- Desktop: each profile's ⋯ menu has **Test for leaks**, which opens IP, WebRTC and DNS leak test pages through that profile.

## Browser profiles: shared or per region

A browser profile is a browser's whole data folder: logins, cookies, saved passwords, history and extensions. Each region uses one of two kinds, and both survive disconnecting and relaunching:

| Setting on the region | Data folder | Logins |
|---|---|---|
| no `chrome_profile` | `~/.porthole/profiles/<region>/<browser>/` | this region only |
| `chrome_profile = "main"` | `~/.porthole/shared/main/<browser>/` | shared with every region that names `main` |

The setting covers Chrome, Brave and Firefox, each with its own folder. Safari always uses your normal Safari data.

**Share one profile across every region:** put the name under `[defaults]`:

```toml
[defaults]
chrome_profile = "main"
```

**Give one region its own logins while the rest share "main":** set a different name on that region. A region's own value beats `[defaults]`:

```toml
[profile.uk]
chrome_profile = "uk"       # ~/.porthole/shared/uk/ - UK logins kept apart
```

**Share among a few regions only:** leave `[defaults]` without `chrome_profile` and name a group on the regions that should share:

```toml
[profile.us]
chrome_profile = "work"

[profile.ca]
chrome_profile = "work"     # us and ca share; every other region keeps its own data
```

- **Start fresh:** pick an unused name. The folder is created empty on first launch, so you sign in once.
- **Keep an existing profile's logins under a new name:** quit that browser, then copy its folder. This works on the same machine and user, because the browser's encryption key lives in the OS keychain, not in the folder:
  ```bash
  mkdir -p ~/.porthole/shared/uk-old && cp -R ~/.porthole/profiles/uk/chrome ~/.porthole/shared/uk-old/
  ```
- **Reset a profile** (log out of everything): quit the browser and delete its folder, e.g. `rm -rf ~/.porthole/shared/main/chrome`.
- **Renaming a region** in the desktop app moves its own data folder too, so logins survive.
- **One region at a time per shared profile.** A browser keeps the proxy it started with. If "main" is open through `uk`, launching `us` is refused until you quit that window; otherwise the new window would quietly go out through the UK. To have two regions open side by side, give them different profile names.
- **Sites notice:** the same account signing in from different countries may be flagged by Google, streaming services or banks. Use separate profiles for accounts you don't want linked across regions.

## Safari

Safari can't take a per-window proxy; it only uses the system proxy. So Safari mode:

1. Serves a PAC file on `127.0.0.1:18080` that sends **only one list of domains** (a `[pac_sets]` entry) through the profile. Everything else, for every app, stays direct. `--all` (desktop: **Safari all traffic**) routes everything.
2. Sets it as the network service's automatic proxy configuration. macOS may ask for your admin password.
3. Restarts Safari and **restores your original proxy settings when Safari quits** (CLI: also on Ctrl-C; desktop: also on Stop or Quit).

The example config ships several lists: `streaming`, `check` (leak and exit test sites), `travel` (regional prices), `search` (localised results), and templates for `home` and `work`. The list comes from `--sites`, else the profile's `pac_set`, else `[safari] pac_set`. Include each site's CDN domains, not just the main one; `porthole safari pac <profile>` prints the generated PAC.

Only one Safari session can be active at a time. While it runs, other apps that use the system proxy also send the listed domains through the region. Browser windows opened by Porthole are unaffected because they set their own proxy. If Porthole is killed hard, run `porthole safari restore` (the desktop app also restores a leftover session when it starts).

## Notes

- Streaming services usually block datacenter IPs, so for streaming a VPN provider's locations work better than your own VPS.
- Runtime state lives in `~/.porthole/` (`PORTHOLE_HOME` overrides it). The config is found via `PORTHOLE_CONFIG`, then `profiles.toml` next to the script, then `~/.config/porthole/profiles.toml`.

## License

[MIT](LICENSE)
