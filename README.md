# soloistd

`soloistd` supervises one or more [Spotify Soloist](https://developer.spotify.com/documentation/soloist) instances in a single systemd user service. Each instance can have its own Spotify Connect name and PipeWire destination. The daemon keeps the API key outside the systemd unit, checks for official updates, and recovers from build expiry.

## Install

Every tagged release carries a statically linked `soloistd-linux-amd64` binary, a `.deb`, and an `.rpm`, all built by CI. Install the package matching the distribution with `apt install ./soloistd_*.deb` or `dnf install ./soloistd-*.rpm`, or just put the static binary on your `PATH`.

### Arch Linux (AUR)

```sh
paru -S soloistd
```

Or build the AUR recipe manually with `makepkg -si`.

### Debian and Ubuntu

The packaging lives in `debian/`. Build it from a checkout on Debian 13 (trixie) or newer, or on Ubuntu 25.04 or newer, because the build needs Go 1.24:

```sh
sudo apt install build-essential debhelper dpkg-dev golang-go
dpkg-buildpackage -us -uc -b
sudo apt install ../soloistd_*.deb
```

### Fedora

The spec file lives in `fedora/`. Build it from a checkout:

```sh
sudo dnf install rpm-build rpmdevtools golang gcc
rpmdev-setuptree
git archive --format=tar.gz --prefix=soloistd-0.2.0/ \
  -o ~/rpmbuild/SOURCES/soloistd-0.2.0.tar.gz HEAD
rpmbuild -bb fedora/soloistd.spec
sudo dnf install ~/rpmbuild/RPMS/*/soloistd-*.rpm
```

### From source

Build and place `soloistd` somewhere permanent on your `PATH`:

```sh
go build -o soloistd .
install -Dm755 soloistd ~/.local/bin/soloistd
```

The recommended way to manage instances is through the running supervisor. Install and start the service, then open the terminal helper:

```sh
soloistd service install
soloistd tui
```

The helper shows the configured instances and lets you add or remove them. It asks for an instance ID and Spotify device name. Each instance added through the helper uses the laptop's current default PipeWire output; no destination needs to be entered. It uses the saved shared API key, or prompts for one if needed. To choose a key for every instance added during one helper session, run `soloistd tui --api-key KEY`. That key takes priority over `SOLOIST_API_KEY` and the saved shared key; it does not change existing instances. The supervisor starts a new instance immediately. The service must be running for the helper to connect.

Instances are saved in `config.json` by default and return when the supervisor restarts. To launch temporary instances from the helper, run `soloistd tui --no-persist` (combine it with `--api-key KEY` if desired). Every instance added in that TUI session stays active after the helper exits but disappears when the supervisor restarts. The TUI and `instance list` mark temporary instances. Removing one stops it without changing `config.json`; its Spotify session and cache files remain on disk.

The same actions are available as direct commands. For example, to launch two devices on the default PipeWire output:

```sh
soloistd instance add --name kitchen --device-name "Kitchen"
soloistd instance add --name bedroom --device-name "Bedroom"
soloistd instance list
```

For one temporary instance, use `soloistd instance add --name guest --device-name "Guest" --no-persist`. This does not write the instance or a newly supplied key to `config.json`. A previously saved shared key remains available.

Each instance writes its Soloist stdout and stderr to a separate file at `$XDG_STATE_HOME/soloistd/logs/ID.log` (or `~/.local/state/soloistd/logs/ID.log` when `XDG_STATE_HOME` is unset). The original unnamed instance uses `default.log`. Pass an absolute path to place an instance's log elsewhere:

```sh
soloistd instance add --name kitchen --device-name "Kitchen" --log-path /var/log/soloist/kitchen.log
```

The TUI also prompts for an optional log path. `instance list` prints the log path as its fourth column. Follow a file with `tail -f /path/to/instance.log`. `soloistd service logs` shows supervisor messages in the systemd journal; Soloist output goes to its instance file. Logs are appended across instance restarts and remain after removal. Created files have mode `0600`; arrange rotation or cleanup for long-running instances.

To detect Soloist's exact `login failed: make sure --api-key is valid` message, add `--monitor-api-key` to `instance add` or `setup`, or set `"monitor_api_key": true` for that instance in `config.json`. For the TUI, `--monitor-api-key` applies to every instance added during that helper session. When monitoring is enabled, `instance list` shows `api-key-login-failed` in its fifth column after the message appears; the TUI shows the same status. If Soloist then exits, the supervisor keeps other instances running and retains the failed instance for inspection or removal. Updating the shared key with `soloistd setup --api-key KEY` restarts failed instances that inherit it. The `running` status only means the process is running; it does not prove that the key is valid. The failure status reports Soloist's diagnostic, not independent key validation; other login failures may not be classified.

Omitting `--pipewire-device` lets PipeWire choose the default output. Pass that option only when you want to target a specific destination. The first `instance add` prompts for an API key without echoing it. You can also supply `--api-key` or `SOLOIST_API_KEY` for automation. For saved instances, the manager saves the shared key privately and reuses it for later instances.

To save the key before adding devices, run `soloistd setup --api-key KEY`. This creates or updates `config.json` without asking for a device name or downloading Soloist. You can also create the file yourself with only the shared key:

```json
{"api_key": "your-key"}
```

Instances added later inherit that key. If you create the file yourself, restrict it with `chmod 600 ~/.config/soloistd/config.json`. Running `setup --api-key KEY` again preserves existing instances and restarts running instances that inherit the shared key. Instances with their own `api_key` keep their key. A key passed directly on the command line may be recorded in shell history; use your shell's secure input mechanism when needed.

Instances start immediately. Remove one without stopping the others:

```sh
soloistd instance remove --name kitchen
```

Removal keeps its Spotify session and cache. Adding the same ID again reuses those files. `instance list` shows the active IDs, Spotify device names, PipeWire destinations, and log paths. The commands require a running supervisor, either from the systemd service or `soloistd run`. Restart a service installed with an older version once after upgrading to enable the new log routing.

For compatibility, `soloistd setup` without a key-only invocation still configures a device and downloads Soloist. Its interactive API-key prompt does not echo. The default Spotify Connect device name is `soloistd@<hostname>` (for example, `soloistd@framework13`):

```sh
soloistd setup
```

The unnamed device remains available as the `default` instance. A subsequent `setup --instance ID` still works, but device changes made through `setup` or by editing JSON take effect only after restarting the service. The key-only `setup --api-key KEY` takes effect for running instances immediately. Use `instance add` and `instance remove` for live changes. Instance IDs start with a letter or digit and may then contain letters, digits, underscores, or hyphens; `default` selects the original unnamed device. Use `wpctl status` to inspect available PipeWire outputs. Each named instance gets its own Spotify session and playback cache.

If Soloist is already running in a terminal, stop that process first so the service can acquire its data directory.

Open Spotify on the same local network, open the device picker, and select a configured device. Each Soloist instance stores its own Spotify Connect session for later restarts.

## Commands

```text
soloistd setup [--instance ID] [--device-name NAME] [--pipewire-device DEST] [--log-path FILE] [--monitor-api-key] [--api-key KEY]
                                Configure an instance offline and fetch Soloist;
                                --api-key KEY alone saves the shared key
soloistd run                    Run the foreground supervisor
soloistd tui [--api-key KEY] [--monitor-api-key] [--no-persist]
                                Interactively view, add, and remove instances
soloistd instance add --name ID --device-name NAME [--pipewire-device DEST] [--log-path FILE] [--monitor-api-key] [--api-key KEY] [--no-persist]
                                Start an instance; save it unless --no-persist
soloistd instance remove --name ID
                                Stop and remove an instance; keep its data
soloistd instance list          List instances, log paths, and status, marking temporary ones
soloistd pair [--instance ID]   Replace one saved Spotify Connect session
soloistd update                 Fetch the current official Soloist build
soloistd ctl [--instance ID] status
                                Run a `soloist ctl` command for one instance
soloistd service status         Show user-service status
soloistd service logs           Follow supervisor messages in the user-service journal
soloistd service restart        Restart the user service
soloistd service uninstall      Remove only the unit; keep config and data
```

With multiple instances, `pair` and `ctl` require `--instance ID` (use `default` for the original unnamed device). Put the option before the `ctl` command, for example `soloistd ctl --instance kitchen status`. Stop the installed service before using `soloistd pair --instance kitchen`, because Soloist permits only one process per data directory. Start the service again after pairing.

## Files and security

| Purpose | Default path |
| --- | --- |
| Private configuration | `$XDG_CONFIG_HOME/soloistd/config.json` |
| Live management socket | `$XDG_CONFIG_HOME/soloistd/control.sock` |
| Managed Soloist binary | `$XDG_DATA_HOME/soloistd/bin/soloist` |
| Installed release metadata | `$XDG_DATA_HOME/soloistd/release.json` |
| Original unnamed instance state | `$XDG_DATA_HOME/soloist` |
| Original unnamed instance cache | `$XDG_CACHE_HOME/soloist` |
| Named instance state | `$XDG_DATA_HOME/soloistd/instances/ID` |
| Named instance cache | `$XDG_CACHE_HOME/soloistd/ID` |
| Instance logs | `$XDG_STATE_HOME/soloistd/logs/ID.log` (`default.log` for the unnamed instance) |
| systemd user unit | `$XDG_CONFIG_HOME/systemd/user/soloistd.service` |

The usual `~/.config`, `~/.local/share`, `~/.cache`, and `~/.local/state` fallbacks are used when the XDG variables are unset. The config directory is mode `0700`; both `config.json` and the live management socket are mode `0600`. The manager updates `config.json` internally when you add or remove a saved instance or change the shared API key, so the change survives a restart. The generated unit contains only the path to `soloistd`; it never contains the API key. Soloist itself requires the key as a command-line argument, so the key can still be visible to same-user process inspection while Soloist is running.

Additional options can be set directly in `config.json`: `data_dir`, `cache_dir`, `cache_size_mb`, `pipewire_device`, `log_path`, `monitor_api_key`, `initial_volume`, and `websocket`. The original top-level device settings still work. Named devices live in the `instances` array and inherit the top-level `api_key` unless they specify their own. For example:

```json
{
  "api_key": "your-key",
  "instances": [
    {"name": "kitchen", "device_name": "Kitchen", "pipewire_device": "alsa_output.kitchen", "log_path": "/var/log/soloist/kitchen.log"},
    {"name": "bedroom", "device_name": "Bedroom", "pipewire_device": "alsa_output.bedroom"}
  ]
}
```

Named instances require unique IDs. Their default data and cache paths are separate from each other and from the original unnamed instance. If you override `data_dir` or `cache_dir`, the paths must not overlap another instance's paths. If you configure `websocket`, use a different listen address for each instance.

## Automatic updates

`soloistd` compares the official archive's CDN metadata at daemon startup and every six hours. When the archive changes, it downloads the stable archive matching the current Linux architecture from `soloist-builds.spotifycdn.com`, extracts and executes `soloist --version` as validation, atomically replaces its user-owned managed executable, and gracefully restarts all running Soloist instances. Failed proactive checks do not interrupt playback.

Spotify documents daemon exit status `10` as “build expired.” This remains an independent fallback: `soloistd` unconditionally downloads the current build and restarts whenever Soloist reports status `10`, even if the proactive metadata check failed. It supports `x86_64`, AArch64, and ARMv7.

## Packaging and CI

| Target | Recipe |
| --- | --- |
| Arch Linux | `aur/PKGBUILD` |
| Debian and Ubuntu | `debian/` |
| Fedora | `fedora/soloistd.spec` |
| Manual page | `doc/soloistd.1` |

GitHub Actions builds and tests the program, then builds, lints, installs, and smoke-tests both the `.deb` and the `.rpm` on every push and pull request. Each run uploads the static binary and both packages as downloadable artifacts. Pushing a `v*` tag attaches those packages to the matching GitHub release. `scripts/check-versions.sh` fails the build when the version in `main.go` and the versions in the packaging recipes disagree.

## License

`soloistd` is licensed under the [Mozilla Public License 2.0](LICENSE).
