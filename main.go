// Command osrs-login is a minimal, Bolt-free launcher for Old School RuneScape.
//
// It performs the Jagex-account OAuth2 flow using your real web browser (no
// embedded CEF, so none of Bolt's sandbox/render problems), then launches
// RuneLite with the JX_* session credentials it expects. After the first login
// the refresh token is cached, so subsequent launches need no browser at all.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const version = "0.1.0"

const usage = `osrs-login — minimal Jagex-account launcher for Old School RuneScape

Usage:
  osrs-login            Log in (reusing a saved session if possible) and launch the client
  osrs-login pick       Always show the character menu, then launch the chosen one
  osrs-login login      Force a fresh browser login
  osrs-login logout     Forget the saved session
  osrs-login -h         Show this help

Flags:
  --client <cmd>        Client to launch (default: runelite)
  --character <name>    Launch this character by display name (skips the menu)
`

func main() {
	args := os.Args[1:]
	client := "runelite"
	character := ""
	cmd := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(usage)
			return
		case "-v", "--version":
			fmt.Println("osrs-login", version)
			return
		case "--client":
			i++
			if i < len(args) {
				client = args[i]
			}
		case "--character":
			i++
			if i < len(args) {
				character = args[i]
			}
		case "login", "logout", "pick":
			cmd = args[i]
		default:
			fmt.Fprintf(os.Stderr, "unknown argument: %s\n\n%s", args[i], usage)
			os.Exit(2)
		}
	}

	if cmd == "logout" {
		if err := clearCreds(); err != nil {
			fatal(err)
		}
		fmt.Println("Logged out (saved session removed).")
		return
	}

	if err := run(cmd == "login", cmd == "pick", client, character); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// run authenticates, obtains a game session, selects a character, and launches.
// forcePick always shows the character menu instead of reusing the remembered one.
func run(forceLogin, forcePick bool, client, character string) error {
	creds, _ := loadCreds()

	// Fast path: reuse a stored game session — no browser, no OAuth.
	if !forceLogin && creds.SessionID != "" {
		if accounts, err := listAccounts(creds.SessionID); err == nil && len(accounts) > 0 {
			fmt.Fprintln(os.Stderr, "reusing saved game session")
			return finishLaunch(client, character, forcePick, creds.SessionID, accounts, &creds)
		}
	}

	tokens, err := authenticate(forceLogin, creds.RefreshToken)
	if err != nil {
		return err
	}
	creds.RefreshToken = tokens.RefreshToken // refresh tokens rotate; keep the latest

	sessionID, err := obtainSession(tokens)
	if err != nil {
		return err
	}
	creds.SessionID = sessionID

	accounts, err := listAccounts(sessionID)
	if err != nil {
		return fmt.Errorf("listing characters: %w", err)
	}
	if len(accounts) == 0 {
		return fmt.Errorf("no characters found on this account")
	}
	return finishLaunch(client, character, forcePick, sessionID, accounts, &creds)
}

// obtainSession turns OAuth tokens into a game session id, trying the cheapest
// path first: (a) the login id_token directly, (b) a server-side consent
// exchange, (c) an interactive browser consent (always works).
func obtainSession(tokens Tokens) (string, error) {
	if sid, err := createSession(tokens.IDToken); err == nil {
		fmt.Fprintln(os.Stderr, "game session created from login token")
		return sid, nil
	}
	if game, err := consentExchange(tokens.IDToken); err == nil {
		if sid, err := createSession(game); err == nil {
			fmt.Fprintln(os.Stderr, "game session created via server-side consent")
			return sid, nil
		}
	}
	game, err := browserConsent(tokens.IDToken)
	if err != nil {
		return "", err
	}
	sid, err := createSession(game)
	if err != nil {
		return "", fmt.Errorf("creating game session: %w", err)
	}
	fmt.Fprintln(os.Stderr, "game session created via browser consent")
	return sid, nil
}

// browserConsent runs the consent step in the user's browser (which carries the
// login session cookie) and reads the game-scoped id_token from the
// http://localhost URL the browser is redirected to.
func browserConsent(idToken string) (string, error) {
	state, nonce := randomString(16), randomString(16)
	u := consentURL(idToken, state, nonce)
	fmt.Println("\nOne more step — authorizing your game session.")
	openBrowser(u)
	fmt.Println("If your browser didn't open, paste this URL into it:")
	fmt.Println("  " + u)
	fmt.Println("\nYour browser will try to open a http://localhost page and fail to")
	fmt.Println("connect — that is expected. Copy that http://localhost#... address from")
	fmt.Print("the address bar and paste it here:\n> ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return tokenFromConsentRedirect(line, state, nonce)
}

// finishLaunch selects a character, persists state, and execs the client.
func finishLaunch(client, character string, forcePick bool, sessionID string, accounts []Account, creds *Creds) error {
	acc, err := chooseAccount(accounts, character, forcePick, creds)
	if err != nil {
		return err
	}
	creds.AccountID = acc.AccountID
	if err := saveCreds(*creds); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not save session:", err)
	}
	fmt.Printf("Launching %s as %s...\n", client, acc.DisplayName)
	return launch(client, sessionID, acc)
}

// authenticate returns a token set, refreshing a saved session when possible
// and falling back to an interactive browser login otherwise.
func authenticate(forceLogin bool, savedRefresh string) (Tokens, error) {
	if !forceLogin && savedRefresh != "" {
		tokens, err := refresh(savedRefresh)
		if err == nil {
			return tokens, nil
		}
		fmt.Fprintln(os.Stderr, "saved session expired or invalid — logging in again...")
	}
	return interactiveLogin()
}

func interactiveLogin() (Tokens, error) {
	verifier, challenge := pkce()
	state := randomString(16)
	u := loginURL(challenge, state)

	fmt.Println("Opening your browser to log in to your Jagex account...")
	openBrowser(u)
	fmt.Println("\nIf it didn't open, paste this URL into your browser:")
	fmt.Println("  " + u)
	fmt.Println("\nAfter logging in you'll land on a runescape.com page. Copy its full")
	fmt.Print("address from the browser and paste it here, then press Enter:\n> ")

	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	code, err := parseAuthCode(line, state)
	if err != nil {
		return Tokens{}, err
	}
	return exchangeCode(code, verifier)
}

// chooseAccount picks a character: an explicit --character, else the single
// account, else a remembered one, else an interactive prompt.
func chooseAccount(accounts []Account, wantName string, forcePick bool, creds *Creds) (Account, error) {
	if wantName != "" {
		for _, a := range accounts {
			if strings.EqualFold(a.DisplayName, wantName) {
				return a, nil
			}
		}
		return Account{}, fmt.Errorf("no character named %q (have: %s)", wantName, accountNames(accounts))
	}
	if len(accounts) == 1 {
		return accounts[0], nil
	}
	if !forcePick && creds.AccountID != "" {
		for _, a := range accounts {
			if a.AccountID == creds.AccountID {
				return a, nil
			}
		}
	}
	fmt.Println("Choose a character:")
	for i, a := range accounts {
		name := a.DisplayName
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("  [%d] %s\n", i+1, name)
	}
	fmt.Print("> ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(accounts) {
		return Account{}, fmt.Errorf("invalid choice")
	}
	return accounts[n-1], nil
}

func accountNames(accounts []Account) string {
	names := make([]string, len(accounts))
	for i, a := range accounts {
		names[i] = a.DisplayName
	}
	return strings.Join(names, ", ")
}

func openBrowser(u string) {
	_ = exec.Command("xdg-open", u).Start()
}

// launch sets the JX_* credentials in the environment and execs the client,
// replacing this process (mirroring Bolt's launcher handoff).
func launch(client, sessionID string, acc Account) error {
	path, err := exec.LookPath(client)
	if err != nil {
		return fmt.Errorf("client %q not found on PATH: %w", client, err)
	}
	env := append(os.Environ(),
		"JX_SESSION_ID="+sessionID,
		"JX_CHARACTER_ID="+acc.AccountID,
		"JX_DISPLAY_NAME="+acc.DisplayName,
	)
	return syscall.Exec(path, []string{path}, env)
}
