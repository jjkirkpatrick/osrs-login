# osrs-login

A tiny, **Bolt-free** launcher for **Old School RuneScape** on Linux. It does the
Jagex-account login in your **normal web browser** (no embedded CEF browser), then
launches **RuneLite** with the credentials it needs. After the first login it
caches the game session, so later launches start the game with no browser at all.

> ⚠️ **Unofficial & unsupported.** Not affiliated with, authored by, or endorsed
> by Jagex. RuneScape and Old School RuneScape are trademarks of Jagex Ltd. This
> is a reverse-engineered interop client — it may break when Jagex changes their
> auth flow, and you use it **at your own risk**.

Built because Bolt's embedded login browser crashes / shows a blank screen on
some setups (notably NixOS + NVIDIA + Wayland), and the official Jagex Launcher
doesn't work under Wine. See [`DESIGN.md`](./DESIGN.md) for how it works and where
the Jagex endpoints come from.

## Usage

```
osrs-login            # log in (reusing a saved session if possible) and launch RuneLite
osrs-login pick       # show the character menu, then launch the chosen character
osrs-login login      # force a fresh browser login
osrs-login logout     # forget the saved session
osrs-login --character Zezima      # launch a specific character (skips the menu)
osrs-login --client hdos           # launch a different client on PATH
osrs-login --client "flatpak run net.runelite.RuneLite"   # client command with arguments
osrs-login pick --picker "fuzzel --dmenu"                # character menu in fuzzel, not the terminal
```

Plain `osrs-login` remembers the last character you picked and launches it
straight away. Use `osrs-login pick` whenever you want to choose a different one
(it reuses the saved session, so no browser).

A cold login (first run, or after the session expires) happens in your browser.
After you log in, the Jagex page opens a `jagex:` link. If the link handler is
installed (see below), the login goes straight back to osrs-login. If not, paste
the `runescape.com` URL you land on into the terminal. Next it opens a consent URL.
Paste its `http://localhost#…` result back (a "can't connect" page is normal).
After that the game session is cached and launches need no browser at all.

`runelite` (or the first word of whatever `--client` you pass) must be on your
`PATH`.

### App-launcher entry (no terminal)

`--picker` shows the character menu with any dmenu-style program. `--terminal`
opens a terminal only when a login actually needs typing (first run, or the
session expired). A launcher entry like this one never opens a terminal on a
normal launch:

```ini
[Desktop Entry]
Type=Application
Name=Old School RuneScape
Exec=osrs-login pick --picker "fuzzel --dmenu" --terminal "kitty -e"
Terminal=false
Categories=Game;
```

(rofi: `--picker "rofi -dmenu"`; foot: `--terminal foot`.) Both values are split
on spaces, so arguments can't contain spaces.

### The `jagex:` link handler

`dist/osrs-login-handler.desktop` handles `jagex:` links, which removes the
first login paste. The Nix package installs it. Elsewhere:

```sh
cp dist/osrs-login-handler.desktop ~/.local/share/applications/
xdg-mime default osrs-login-handler.desktop x-scheme-handler/jagex
update-desktop-database ~/.local/share/applications
```

If the official Jagex Launcher is installed it may already own `jagex:` links;
the `xdg-mime` line above takes them back.

## Build / run

With Nix (flakes):

```sh
nix run .                 # build and run
nix build .               # produces ./result/bin/osrs-login
```

With the Go toolchain:

```sh
go install github.com/jjkirkpatrick/osrs-login@latest
# or, in a clone:
go test ./... && go build -o osrs-login . && ./osrs-login
```

## Install on NixOS

Add it as a flake input and put the package in your environment, e.g.:

```nix
# flake.nix inputs
osrs-login.url = "github:jjkirkpatrick/osrs-login";

# somewhere in your packages list
inputs.osrs-login.packages.${pkgs.system}.default
```

Then `osrs-login` is on your `PATH`. Pair it with `runelite` (already in
nixpkgs).

## Credentials & security

The cached refresh token grants access to your account. It's stored at
`$XDG_DATA_HOME/osrs-login/creds.json` (default `~/.local/share/osrs-login/`)
with `0600` permissions — the same plaintext model Bolt uses. Run
`osrs-login logout` to delete it.

## Status

v1: OSRS + RuneLite, single account. The login is validated end-to-end against
the live Jagex API.

## Acknowledgements & licensing

This is an independent reimplementation. The Jagex OAuth endpoints, client IDs,
and request parameters were determined by inspecting the open-source
[Bolt](https://github.com/Adamcake/Bolt) launcher (AGPL-3.0) — these are factual
interoperability constants, and **this project contains no Bolt source code**.
Thanks to the Bolt authors for documenting the flow.

osrs-login itself is licensed [MIT](./LICENSE).
