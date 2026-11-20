# osrs-login — design

A minimal, Bolt-free launcher for Old School RuneScape on Linux. It replicates
the Jagex-account OAuth2 flow that the (now-archived) [Bolt](https://github.com/Adamcake/Bolt)
launcher uses, but does the interactive steps in the user's **real browser**
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

**First login (cold)**

1. Generate PKCE (S256) + state; open the browser to `/oauth2/auth`
   (`response_type=code`, scope `openid offline gamesso.token.create user.profile.read`).
2. User logs in → lands on `secure.runescape.com/...launcher-redirect?code=…`,
   which opens `jagex:code=…,state=…,intent=social_auth`. If
   `osrs-login-handler.desktop` owns `x-scheme-handler/jagex`, a second
   `osrs-login jagex:…` process forwards that URI over a unix socket
   (`$XDG_RUNTIME_DIR/osrs-login.sock`) to the waiting prompt. Otherwise the user
   **pastes the URL**. Either way we check `state` and extract `code`.
3. `POST /oauth2/token` (`grant_type=authorization_code`, `code_verifier`) →
   `access_token`, `id_token`, `refresh_token`.
4. **Consent (browser):** open `/oauth2/auth` (consent client_id,
   `response_type=id_token code`, `redirect_uri=http://localhost`) in the
   browser; it redirects to `http://localhost#id_token=…` (a failed page load).
   User **pastes that URL**; we pull the game-scoped `id_token` from the fragment.
5. `POST /game-session/v1/sessions` `{"idToken": …}` → `sessionId`.
6. `GET /game-session/v1/accounts` (`Authorization: Bearer <sessionId>`) →
   characters.
7. Pick a character; set `JX_SESSION_ID`, `JX_CHARACTER_ID` (= `accountId`),
   `JX_DISPLAY_NAME`; `exec runelite`.

**Subsequent launches:** reuse the cached `sessionId` — `listAccounts` it; if
valid, jump straight to steps 6–7. No browser, no paste. Only when the session
has expired do we fall back to the full login above.

### The consent step (confirmed live)

Bolt runs the consent step inside its browser, which carries the login session
cookies. We try three paths cheapest-first in `obtainSession`:

- **(a)** `POST /sessions` with the login `id_token` directly — *fails* live
  (the launcher-audience token isn't accepted by the game-session API).
- **(b)** server-side consent (`GET /oauth2/auth` + `id_token_hint`, read the
  `Location` header) — *fails* live with **HTTP 403**; Jagex requires the browser
  login-session cookie, which a server-side call doesn't have.
- **(c)** **browser consent** — open the consent URL in the user's browser (it
  has the cookie), let it redirect to `http://localhost#id_token=…` (a
  "can't connect" page is expected), and paste that URL back. This **works**.

So a **cold login is two pastes** (the login redirect URL, then the
`http://localhost#…` consent URL). Paths (a)/(b) are kept in case Jagex ever
loosens them. With the `jagex:` handler installed, the first paste goes away.

## Components (one Go package, no external deps)

- `jagex.go` — constants, types, PKCE, URL building, token exchange/refresh,
  consent, session creation, account listing, response parsing.
- `store.go` — load/save/clear `$XDG_DATA_HOME/osrs-login/creds.json`
  (`{refresh_token, session_id, account_id}`, mode `0600`).
- `main.go` — CLI parsing (`pick`/`login`/`logout` + `--client`/`--character`),
  orchestration, character selection, `exec` handoff.
- `jagex_test.go` — unit tests for the pure logic (PKCE, URL/redirect parsing,
  JSON parsing). Live OAuth validated by running it end-to-end.

## Storage & trust

The refresh token and session id grant account access; stored in plaintext at
mode `0600`, the same trust model Bolt uses. `osrs-login logout` removes them.

## Commands

- `osrs-login` — reuse the saved session and launch the remembered character.
- `osrs-login pick` — reuse the saved session but always show the character menu.
- `osrs-login login` — force a fresh browser login.
- `osrs-login logout` — forget the saved session.
- `--client <cmd>` / `--character <name>` — override the client or skip the menu.
- `--picker <cmd>` — show the character menu with a dmenu-style command.
- `--terminal <cmd>` — when a login prompt has no tty, re-exec inside this.

## Scope

**v1:** OSRS + RuneLite, single Jagex account, character remembered after first
choice (re-choose with `pick`). **Non-goals (easy to add later):** RS3, HDOS, the
official client, a GUI, multi-account quick-switching.

## Packaging

A flake exposing `packages.default` (`buildGoModule`, `vendorHash = null`) and a
`go` devShell. Consumed from the user's NixOS config by adding this repo as a
flake input. `postInstall` copies `dist/*.desktop` (the `jagex:` handler) into
`share/applications`. No launcher entry is shipped, because it depends on the
user's picker and terminal (`--picker`, `--terminal`; see the README).
