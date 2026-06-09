# osrs-login

A tiny, **Bolt-free** launcher for **Old School RuneScape** on Linux. It does the
Jagex-account login in your **normal web browser** (no embedded CEF browser), then
launches **RuneLite** with the credentials it needs. After the first login it
caches a refresh token, so later launches start the game with no browser at all.

Built because Bolt's embedded login browser crashes / shows a blank screen on
some setups (notably NixOS + NVIDIA + Wayland), and the official Jagex Launcher
doesn't work under Wine. See [`DESIGN.md`](./DESIGN.md) for how it works and where
the Jagex endpoints come from.

## Usage

```
osrs-login            # log in (reusing a saved session if possible) and launch RuneLite
osrs-login login      # force a fresh browser login
osrs-login logout     # forget the saved session
osrs-login --character Zezima      # launch a specific character
osrs-login --client hdos           # launch a different client binary on PATH
```

First run: a browser tab opens for the Jagex login. After you log in you'll land
on a `runescape.com` page — copy its full address from the URL bar and paste it
back into the terminal. That's the only manual step, and only on first login.

`runelite` (or whatever `--client` you pass) must be on your `PATH`.

## Build / run

With Nix (flakes):

```sh
nix run .                 # build and run
nix build .               # produces ./result/bin/osrs-login
```

With the Go toolchain:

```sh
go test ./...
go build -o osrs-login .
./osrs-login
```

## Install on NixOS

Add it as a flake input and put the package in your environment, e.g.:

```nix
# flake.nix inputs
osrs-login.url = "git+file:///home/josh/repos/osrs-login";   # or a git remote

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

v1: OSRS + RuneLite, single account. Unofficial and reverse-engineered from
Bolt's (open) source — if Jagex changes their auth flow it may need updating. Not
affiliated with or endorsed by Jagex.
