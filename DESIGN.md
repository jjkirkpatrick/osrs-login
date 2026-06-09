# osrs-login — design

A minimal, Bolt-free launcher for Old School RuneScape on Linux. It replicates
the Jagex-account OAuth2 flow that the (now-archived) [Bolt](https://github.com/Adamcake/Bolt)
launcher uses, but does the one interactive step in the user's **real browser**
instead of an embedded CEF browser — sidestepping the sandbox/Wayland/NVIDIA
render bugs that make Bolt's login fail on some setups.

## Why

OSRS Jagex accounts can only authenticate through a launcher that performs the
OAuth flow and hands credentials to the client via `JX_*` environment variables.
RuneLite alone cannot do the Jagex login. On Linux the options are Bolt (embedded
CEF — crashes/blank-screens for some) or the official Windows launcher under Wine
(broken: WebView2/IE error). This tool is a third option: a tiny CLI that uses a
normal browser tab for login.

## Flow

Constants are lifted verbatim from Bolt's source (`src/browser/app.cxx`,
`app/src/lib/Services/AuthService.ts`):

| Purpose | Value |
| --- | --- |
| OAuth origin | `https://account.jagex.com` (`/oauth2/auth`, `/oauth2/token`) |
| Launcher client_id | `com_jagex_auth_desktop_launcher` |
| Login redirect | `https://secure.runescape.com/m=weblogin/launcher-redirect` |
| Consent client_id | `1fddee4e-b100-4f4e-b2b0-097f9088f9d2` (redirect `http://localhost`) |
| Game session API | `https://auth.jagex.com/game-session/v1` (`/sessions`, `/accounts`) |

**First login**

1. Generate PKCE (S256) + state; open the browser to `/oauth2/auth`
   (`response_type=code`, scope `openid offline gamesso.token.create user.profile.read`).
2. User logs in → lands on `secure.runescape.com/...launcher-redirect?code=…`.
   User **pastes that URL** into the terminal; we extract `code`.
3. `POST /oauth2/token` (`grant_type=authorization_code`, `code_verifier`) →
   `access_token`, `id_token`, `refresh_token`.
4. **Consent (server-side, no browser):** `GET /oauth2/auth` with
   `id_token_hint`, the consent client_id, `response_type=id_token code`,
   `redirect_uri=http://localhost`, *without following redirects*. Read the
   `Location` header and pull the game-scoped `id_token` from its query/fragment.
5. `POST /game-session/v1/sessions` `{"idToken": …}` → `sessionId`.
6. `GET /game-session/v1/accounts` (`Authorization: Bearer <sessionId>`) →
   characters.
7. Pick a character; set `JX_SESSION_ID`, `JX_CHARACTER_ID` (= `accountId`),
   `JX_DISPLAY_NAME`; `exec runelite`.

**Subsequent launches:** load cached refresh token → `refresh` → steps 4–7. No
browser, no paste. If the refresh fails, fall back to the browser login.

### The one reverse-engineered assumption

Bolt runs the consent step (step 4) inside its browser, which carries the login
session cookies. We do it server-side relying solely on `id_token_hint`. This is
the standard OIDC mechanism and the expected behaviour, but it is the single part
that must be confirmed against the live server on first run. If the consent
endpoint refuses without a browser session, the fallback is to open the consent
URL in the browser and paste the resulting `http://localhost#…` URL (the code
already parses both query and fragment).

## Components (one Go package, no external deps)

- `jagex.go` — constants, types, PKCE, URL building, token exchange/refresh,
  consent, session creation, account listing, response parsing.
- `store.go` — load/save/clear `$XDG_DATA_HOME/osrs-login/creds.json`
  (`{refresh_token, account_id}`, mode `0600`).
- `main.go` — CLI parsing, orchestration, character selection, `exec` handoff.
- `jagex_test.go` — unit tests for the pure logic (PKCE, URL/redirect parsing,
  JSON parsing). Live OAuth is validated by running it once.

## Storage & trust

The refresh token grants account access; stored in plaintext at mode `0600`,
the same trust model Bolt uses. `osrs-login logout` removes it.

## Scope

**v1:** OSRS + RuneLite, single Jagex account, character remembered after first
choice. **Non-goals (easy to add later):** RS3, HDOS, the official client, a GUI,
multi-account quick-switching.

## Packaging

A flake exposing `packages.default` (`buildGoModule`, `vendorHash = null`) and a
`go` devShell. Consumed from the user's NixOS config either by adding this repo
as a flake input, or building it locally. A `.desktop` entry can wrap
`osrs-login` for the app launcher.
