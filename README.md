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

The recommended way to manage instances is through the running supervisor. Install and start the service, then add devices without restarting it:

```sh
soloistd service install
soloistd instance add --name kitchen --device-name "Kitchen" --pipewire-device alsa_output.kitchen
soloistd instance add --name bedroom --device-name "Bedroom" --pipewire-device alsa_output.bedroom
soloistd instance list
```

Replace the example PipeWire destinations with destinations on your machine. The first `instance add` prompts for an API key without echoing it. You can also supply `--api-key` or `SOLOIST_API_KEY` for automation. The manager saves the key privately and reuses it for later instances.

To save the key before adding devices, run `soloistd setup --api-key KEY`. This creates or updates `config.json` without asking for a device name or downloading Soloist. You can also create the file yourself with only the shared key:

```json
{"api_key": "your-key"}
```

Instances added later inherit that key. If you create the file yourself, restrict it with `chmod 600 ~/.config/soloistd/config.json`. Running `setup --api-key KEY` again preserves existing instances and restarts running instances that inherit the shared key. Instances with their own `api_key` keep their key. A key passed directly on the command line may be recorded in shell history; use your shell's secure input mechanism when needed.

Instances start immediately. Remove one without stopping the others:

```sh
soloistd instance remove --name kitchen
```

Removal keeps its Spotify session and cache. Adding the same ID again reuses those files. `instance list` shows the configured IDs, Spotify device names, and PipeWire destinations. The commands require a running supervisor, either from the systemd service or `soloistd run`. After upgrading an already running service from a version without instance commands, restart it once to enable live management.

For compatibility, `soloistd setup` without a key-only invocation still configures a device and downloads Soloist. Its interactive API-key prompt does not echo. The default Spotify Connect device name is `soloistd@<hostname>` (for example, `soloistd@framework13`):

```sh
soloistd setup
```

The unnamed device remains available as the `default` instance. A subsequent `setup --instance ID` still works, but device changes made through `setup` or by editing JSON take effect only after restarting the service. The key-only `setup --api-key KEY` takes effect for running instances immediately. Use `instance add` and `instance remove` for live changes. Instance IDs start with a letter or digit and may then contain letters, digits, underscores, or hyphens; `default` selects the original unnamed device. Use `wpctl status` to inspect available PipeWire outputs. Each named instance gets its own Spotify session and playback cache.

If Soloist is already running in a terminal, stop that process first so the service can acquire its data directory.

Open Spotify on the same local network, open the device picker, and select a configured device. Each Soloist instance stores its own Spotify Connect session for later restarts.

## Commands

```text
soloistd setup [--instance ID] [--device-name NAME] [--pipewire-device DEST] [--api-key KEY]
                                Configure an instance offline and fetch Soloist;
                                --api-key KEY alone saves the shared key
soloistd run                    Run the foreground supervisor
soloistd instance add --name ID --device-name NAME [--pipewire-device DEST] [--api-key KEY]
                                Start and save an instance immediately
soloistd instance remove --name ID
                                Stop and remove an instance; keep its data
soloistd instance list          List configured instances
soloistd pair [--instance ID]   Replace one saved Spotify Connect session
soloistd update                 Fetch the current official Soloist build
soloistd ctl [--instance ID] status
                                Run a `soloist ctl` command for one instance
soloistd service status         Show user-service status
soloistd service logs           Follow user-service logs
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
| systemd user unit | `$XDG_CONFIG_HOME/systemd/user/soloistd.service` |

The usual `~/.config`, `~/.local/share`, and `~/.cache` fallbacks are used when the XDG variables are unset. The config directory is mode `0700`; both `config.json` and the live management socket are mode `0600`. The manager updates `config.json` internally when you add or remove an instance or change the shared API key, so the change survives a restart. The generated unit contains only the path to `soloistd`; it never contains the API key. Soloist itself requires the key as a command-line argument, so the key can still be visible to same-user process inspection while Soloist is running.

Additional documented Soloist options can be set directly in `config.json`: `data_dir`, `cache_dir`, `cache_size_mb`, `pipewire_device`, `initial_volume`, and `websocket`. The original top-level device settings still work. Named devices live in the `instances` array and inherit the top-level `api_key` unless they specify their own. For example:

```json
{
  "api_key": "your-key",
  "instances": [
    {"name": "kitchen", "device_name": "Kitchen", "pipewire_device": "alsa_output.kitchen"},
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
