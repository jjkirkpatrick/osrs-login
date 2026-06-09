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

const usage = `osrs-login — minimal Jagex-account launcher for Old School RuneScape

Usage:
  osrs-login            Log in (reusing a saved session if possible) and launch the client
  osrs-login login      Force a fresh browser login
  osrs-login logout     Forget the saved session
  osrs-login -h         Show this help

Flags:
  --client <cmd>        Client to launch (default: runelite)
  --character <name>    Launch this character by display name (otherwise remembered/prompted)
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
		case "login", "logout":
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

	if err := run(cmd == "login", client, character); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// run authenticates (refresh or interactive), selects a character, and launches.
func run(forceLogin bool, client, character string) error {
	creds, _ := loadCreds()

	tokens, err := authenticate(forceLogin, creds.RefreshToken)
	if err != nil {
		return err
	}

	gameToken, err := consent(tokens.IDToken)
	if err != nil {
		return fmt.Errorf("consent step: %w", err)
	}
	sessionID, err := createSession(gameToken)
	if err != nil {
		return fmt.Errorf("creating game session: %w", err)
	}
	accounts, err := listAccounts(sessionID)
	if err != nil {
		return fmt.Errorf("listing characters: %w", err)
	}
	if len(accounts) == 0 {
		return fmt.Errorf("no characters found on this account")
	}

	creds.RefreshToken = tokens.RefreshToken // refresh tokens rotate; keep the latest
	acc, err := chooseAccount(accounts, character, &creds)
	if err != nil {
		return err
	}
	creds.AccountID = acc.AccountID
	if err := saveCreds(creds); err != nil {
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
	u := loginURL(challenge, randomString(16))

	fmt.Println("Opening your browser to log in to your Jagex account...")
	openBrowser(u)
	fmt.Println("\nIf it didn't open, paste this URL into your browser:")
	fmt.Println("  " + u)
	fmt.Println("\nAfter logging in you'll land on a runescape.com page. Copy its full")
	fmt.Print("address from the browser and paste it here, then press Enter:\n> ")

	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	code, err := parseAuthCode(line)
	if err != nil {
		return Tokens{}, err
	}
	return exchangeCode(code, verifier)
}

// chooseAccount picks a character: an explicit --character, else the single
// account, else a remembered one, else an interactive prompt.
func chooseAccount(accounts []Account, wantName string, creds *Creds) (Account, error) {
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
	if creds.AccountID != "" {
		for _, a := range accounts {
			if a.AccountID == creds.AccountID {
				return a, nil
			}
		}
	}
	fmt.Println("Choose a character:")
	for i, a := range accounts {
		fmt.Printf("  [%d] %s\n", i+1, a.DisplayName)
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
