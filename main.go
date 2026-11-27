// Command osrs-login is a minimal, Bolt-free launcher for Old School RuneScape
// (and, with --game rs3, RuneScape 3).
//
// It performs the Jagex-account OAuth2 flow using your real web browser (no
// embedded CEF, so none of Bolt's sandbox/render problems), then launches
// RuneLite with the JX_* session credentials it expects. After the first login
// the refresh token is cached, so subsequent launches need no browser at all.
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"
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
  --client <cmd>        Client command to launch, may include arguments
                        (default: runelite; e.g. "flatpak run net.runelite.RuneLite")
  --game <osrs|rs3>     Which game to launch (default: osrs). rs3 defaults the client
                        to /usr/share/games/runescape-launcher/runescape
                        (from Jagex's Linux .deb)
  --character <name>    Launch this character by display name (skips the menu)
  --picker <cmd>        Show the character menu with a dmenu-style command
                        (names on stdin, choice on stdout), e.g. "fuzzel --dmenu"
  --terminal <cmd>      When a login needs typing but there's no terminal (e.g.
                        started from an app launcher), re-run inside this,
                        e.g. "kitty -e"
`

// Set from flags; package-level because they only matter deep in the prompts.
var (
	pickerCmd   string
	terminalCmd string
	rs3         bool
)

// rs3ConfigURI is what the RS3 client is pointed at; the same value Bolt passes.
const rs3ConfigURI = "https://www.runescape.com/k=5/l=0/jav_config.ws"

func main() {
	args := os.Args[1:]

	// Invoked by the desktop as the jagex: scheme handler: forward the URI to the
	// osrs-login waiting at its login prompt, then exit.
	if len(args) == 1 && strings.HasPrefix(args[0], "jagex:") {
		if err := sendHandoff(args[0]); err != nil {
			fatal(fmt.Errorf("no osrs-login is waiting for a login (%w)", err))
		}
		return
	}
	client := ""
	game := "osrs"
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
		case "--picker", "--terminal":
			flag := args[i]
			i++
			if i < len(args) {
				if flag == "--picker" {
					pickerCmd = args[i]
				} else {
					terminalCmd = args[i]
				}
			}
		case "--game":
			i++
			if i < len(args) {
				game = args[i]
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

	if game != "osrs" && game != "rs3" {
		fatal(fmt.Errorf("unknown --game %q (want osrs or rs3)", game))
	}
	rs3 = game == "rs3"
	if client == "" {
		client = "runelite"
		if rs3 {
			// The real client, not the .deb's runescape-launcher script: that
			// takes the config URI as a bare first argument, not --configURI.
			client = "/usr/share/games/runescape-launcher/runescape"
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

	// Fast path: reuse a stored game session — no browser, no OAuth. Only an
	// actual rejection means "expired"; a network error shouldn't force a login.
	if !forceLogin && creds.SessionID != "" {
		accounts, err := listAccounts(creds.SessionID)
		if err == nil && len(accounts) > 0 {
			fmt.Fprintln(os.Stderr, "reusing saved game session")
			return finishLaunch(client, character, forcePick, creds.SessionID, accounts, &creds)
		}
		if err != nil && !rejected(err) {
			return fmt.Errorf("checking saved session: %w", err)
		}
	}

	tokens, err := authenticate(forceLogin, creds.RefreshToken)
	if err != nil {
		return err
	}
	// Refresh tokens rotate (the old one is now dead), so persist the new one
	// immediately — before anything below can fail and lose it.
	if tokens.RefreshToken != "" {
		creds.RefreshToken = tokens.RefreshToken
		persist(creds)
	}

	sessionID, err := obtainSession(tokens)
	if err != nil {
		return err
	}
	creds.SessionID = sessionID
	persist(creds)

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
	if err := ensureTerminal(); err != nil {
		return "", err
	}
	state, nonce := randomString(16), randomString(16)
	u := consentURL(idToken, state, nonce)
	fmt.Println("\nOne more step — authorizing your game session.")
	openBrowser(u)
	fmt.Println("If your browser didn't open, paste this URL into it:")
	fmt.Println("  " + u)
	fmt.Println("\nYour browser will try to open a http://localhost page and fail to")
	fmt.Println("connect — that is expected. Copy that http://localhost#... address from")
	fmt.Print("the address bar and paste it here:\n> ")
	return tokenFromConsentRedirect(readLine(), state, nonce)
}

// finishLaunch selects a character, persists state, and execs the client.
func finishLaunch(client, character string, forcePick bool, sessionID string, accounts []Account, creds *Creds) error {
	acc, err := chooseAccount(accounts, character, forcePick, creds)
	if err != nil {
		return err
	}
	creds.AccountID = acc.AccountID
	persist(*creds)
	fmt.Printf("Launching %s as %s...\n", client, acc.DisplayName)
	return launch(client, sessionID, acc)
}

// persist saves creds, warning (not failing) if it can't.
func persist(c Creds) {
	if err := saveCreds(c); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not save session:", err)
	}
}

// authenticate returns a token set, refreshing a saved session when possible
// and falling back to an interactive browser login otherwise.
func authenticate(forceLogin bool, savedRefresh string) (Tokens, error) {
	if !forceLogin && savedRefresh != "" {
		tokens, err := refresh(savedRefresh)
		if err == nil {
			return tokens, nil
		}
		if !rejected(err) {
			return Tokens{}, fmt.Errorf("refreshing saved session: %w", err)
		}
		fmt.Fprintln(os.Stderr, "saved session expired or invalid — logging in again...")
	}
	return interactiveLogin()
}

func interactiveLogin() (Tokens, error) {
	if err := ensureTerminal(); err != nil {
		return Tokens{}, err
	}
	verifier, challenge := pkce()
	state := randomString(16)
	u := loginURL(challenge, state)

	fmt.Println("Opening your browser to log in to your Jagex account...")
	openBrowser(u)
	fmt.Println("\nIf it didn't open, paste this URL into your browser:")
	fmt.Println("  " + u)
	fmt.Println("\nAfter logging in your browser hands the login back to osrs-login")
	fmt.Println("automatically if the jagex: link handler is installed. Otherwise you'll")
	fmt.Println("land on a runescape.com page: copy its full address from the browser and")
	fmt.Print("paste it here, then press Enter:\n> ")

	code, err := parseAuthCode(readLoginRedirect(), state)
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
	names := make([]string, len(accounts))
	for i, a := range accounts {
		names[i] = a.DisplayName
		if names[i] == "" {
			names[i] = "(unnamed)"
		}
	}
	if pickerCmd != "" {
		i, err := runPicker(names)
		if err != nil {
			return Account{}, err
		}
		return accounts[i], nil
	}
	fmt.Println("Choose a character:")
	for i, name := range names {
		fmt.Printf("  [%d] %s\n", i+1, name)
	}
	fmt.Print("> ")
	n, err := strconv.Atoi(strings.TrimSpace(readLine()))
	if err != nil || n < 1 || n > len(accounts) {
		return Account{}, fmt.Errorf("invalid choice")
	}
	return accounts[n-1], nil
}

// runPicker shows names via the dmenu-style --picker command and returns the
// index of the chosen one.
func runPicker(names []string) (int, error) {
	argv := strings.Fields(pickerCmd)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(strings.Join(names, "\n") + "\n")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("no character chosen (picker: %w)", err)
	}
	choice := strings.TrimSpace(string(out))
	for i, n := range names {
		if n == choice {
			return i, nil
		}
	}
	return 0, fmt.Errorf("picker returned unknown character %q", choice)
}

// isTerminal reports whether stdin is a terminal: TCGETS only succeeds on a tty
// (a char-device check would also pass /dev/null).
func isTerminal() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdin.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// ensureTerminal makes sure a typed-input prompt can be answered. With no
// terminal on stdin it re-runs osrs-login, same arguments, inside --terminal.
// Anything done so far (a rotated refresh token) is already saved, so the
// re-run picks up where this one left off.
func ensureTerminal() error {
	if isTerminal() {
		return nil
	}
	if terminalCmd == "" {
		return fmt.Errorf("logging in needs a terminal: run osrs-login from one, or pass --terminal")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	argv := append(strings.Fields(terminalCmd), self)
	argv = append(argv, os.Args[1:]...)
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("terminal %q not found on PATH: %w", argv[0], err)
	}
	argv[0] = path
	return syscall.Exec(path, argv, os.Environ())
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
// replacing this process (mirroring Bolt's launcher handoff). client may carry
// arguments, split on whitespace (no shell quoting).
func launch(client, sessionID string, acc Account) error {
	argv := strings.Fields(client)
	if len(argv) == 0 {
		return fmt.Errorf("empty --client")
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("client %q not found on PATH: %w", argv[0], err)
	}
	argv[0] = path
	env := append(os.Environ(),
		"JX_SESSION_ID="+sessionID,
		"JX_CHARACTER_ID="+acc.AccountID,
		"JX_DISPLAY_NAME="+acc.DisplayName,
	)
	if rs3 {
		// The RS3 client needs its config URI, and (like Bolt) X11 for SDL so it
		// runs under XWayland. Defaults only: the user's environment wins.
		argv = append(argv, "--configURI", rs3ConfigURI)
		for k, v := range map[string]string{
			"SDL_VIDEODRIVER":       "x11",
			"SDL_VIDEO_X11_WMCLASS": "RuneScape",
			"PULSE_PROP_OVERRIDE":   "application.name='RuneScape' application.icon_name='runescape' media.role='game'",
		} {
			if os.Getenv(k) == "" {
				env = append(env, k+"="+v)
			}
		}
	}
	return syscall.Exec(path, argv, env)
}

// stdin is read by a single goroutine so a prompt can race it against the
// jagex: handoff without losing input to a second buffered reader.
var (
	stdinOnce  sync.Once
	stdinLines = make(chan string)
)

func stdinChan() <-chan string {
	stdinOnce.Do(func() {
		go func() {
			r := bufio.NewReader(os.Stdin)
			for {
				line, err := r.ReadString('\n')
				if line != "" {
					stdinLines <- line
				}
				if err != nil {
					close(stdinLines)
					return
				}
			}
		}()
	})
	return stdinLines
}

// readLine returns the next line of stdin ("" at EOF).
func readLine() string { return <-stdinChan() }

// handoffPath is the socket the login prompt listens on for the jagex: handler.
func handoffPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "osrs-login.sock")
}

// readLoginRedirect waits for the login redirect from whichever arrives first:
// the jagex: scheme handler (via the socket) or a manual paste on stdin. The
// state check in parseAuthCode guards against anything injected on the socket.
func readLoginRedirect() string {
	p := handoffPath()
	_ = os.Remove(p) // stale socket from a crashed run
	ln, err := net.Listen("unix", p)
	if err != nil {
		return readLine()
	}
	defer ln.Close() // also unlinks the socket

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		fmt.Println("(received login from browser)")
		return line
	case line := <-stdinChan():
		return line
	}
}

// sendHandoff forwards a jagex: URI to the waiting login prompt.
func sendHandoff(uri string) error {
	c, err := net.Dial("unix", handoffPath())
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = fmt.Fprintln(c, uri)
	return err
}
