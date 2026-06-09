package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Jagex auth constants, extracted from Bolt's source
// (src/browser/app.cxx and app/src/lib/Services/AuthService.ts).
const (
	authOrigin       = "https://account.jagex.com"
	gameAuthAPI      = "https://auth.jagex.com/game-session/v1"
	launcherClientID = "com_jagex_auth_desktop_launcher"
	launcherRedirect = "https://secure.runescape.com/m=weblogin/launcher-redirect"
	consentClientID  = "1fddee4e-b100-4f4e-b2b0-097f9088f9d2"
	consentRedirect  = "http://localhost"
	loginScope       = "openid offline gamesso.token.create user.profile.read"
	consentScope     = "openid offline"
)

// httpClient follows redirects normally; noRedirect lets us read the Location
// header of the consent step instead of following it.
var (
	httpClient = &http.Client{Timeout: 30 * time.Second}
	noRedirect = &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

// Tokens is an OAuth2 token set.
type Tokens struct {
	AccessToken  string
	IDToken      string
	RefreshToken string
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// Account is a playable character returned by the game-session API.
type Account struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	UserHash    string `json:"userHash"`
}

// pkce returns a PKCE (RFC 7636, S256) verifier and its derived code challenge.
func pkce() (verifier, challenge string) {
	verifier = randomString(32)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// randomString returns a URL-safe token derived from n random bytes.
func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// loginURL builds the interactive authorization URL the user opens in a browser.
func loginURL(challenge, state string) string {
	q := url.Values{
		"auth_method":           {""},
		"login_type":            {""},
		"flow":                  {"launcher"},
		"response_type":         {"code"},
		"client_id":             {launcherClientID},
		"redirect_uri":          {launcherRedirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"login"},
		"scope":                 {loginScope},
		"state":                 {state},
	}
	return authOrigin + "/oauth2/auth?" + q.Encode()
}

// parseAuthCode extracts the `code` parameter from the URL the user is
// redirected to after logging in (and pastes back).
func parseAuthCode(redirectedURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(redirectedURL))
	if err != nil {
		return "", fmt.Errorf("that doesn't look like a URL: %w", err)
	}
	code := mergeQueryFragment(u).Get("code")
	if code == "" {
		return "", errors.New("no `code` found in the pasted URL — did you copy the full address after logging in?")
	}
	return code, nil
}

// mergeQueryFragment returns the union of a URL's query and fragment parameters.
// The consent step (response_type "id_token code") returns its result in the
// fragment, while the login step returns `code` in the query.
func mergeQueryFragment(u *url.URL) url.Values {
	vals := u.Query()
	if u.Fragment != "" {
		if frag, err := url.ParseQuery(u.Fragment); err == nil {
			for k, vs := range frag {
				for _, v := range vs {
					vals.Add(k, v)
				}
			}
		}
	}
	return vals
}

// exchangeCode swaps an authorization code (+ PKCE verifier) for tokens.
func exchangeCode(code, verifier string) (Tokens, error) {
	return postToken(url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {launcherClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {launcherRedirect},
	})
}

// refresh exchanges a refresh token for a fresh token set.
func refresh(refreshToken string) (Tokens, error) {
	return postToken(url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {launcherClientID},
		"refresh_token": {refreshToken},
	})
}

func postToken(form url.Values) (Tokens, error) {
	req, _ := http.NewRequest(http.MethodPost, authOrigin+"/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Tokens{}, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	tokens, err := parseTokenResponse(body)
	if err != nil {
		return Tokens{}, err
	}
	return tokens, nil
}

// parseTokenResponse decodes the JSON body of an /oauth2/token response.
func parseTokenResponse(body []byte) (Tokens, error) {
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return Tokens{}, fmt.Errorf("decoding token response: %w", err)
	}
	if tr.IDToken == "" {
		return Tokens{}, errors.New("token response missing id_token")
	}
	return Tokens{AccessToken: tr.AccessToken, IDToken: tr.IDToken, RefreshToken: tr.RefreshToken}, nil
}

// consent performs the non-interactive consent step, exchanging the login
// id_token for a game-scoped id_token. It is done server-side by reading the
// redirect Location header, so no browser is involved.
func consent(idToken string) (string, error) {
	q := url.Values{
		"id_token_hint": {idToken},
		"nonce":         {randomString(16)},
		"prompt":        {"consent"},
		"redirect_uri":  {consentRedirect},
		"response_type": {"id_token code"},
		"state":         {randomString(16)},
		"client_id":     {consentClientID},
		"scope":         {consentScope},
	}
	resp, err := noRedirect.Get(authOrigin + "/oauth2/auth?" + q.Encode())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("consent step did not redirect (status %d); try `osrs-login login` to re-authenticate", resp.StatusCode)
	}
	lu, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("parsing consent redirect %q: %w", loc, err)
	}
	gameToken := mergeQueryFragment(lu).Get("id_token")
	if gameToken == "" {
		return "", fmt.Errorf("no id_token in consent redirect: %s", loc)
	}
	return gameToken, nil
}

// createSession exchanges a game-scoped id_token for a game session id.
func createSession(gameIDToken string) (string, error) {
	body, _ := json.Marshal(map[string]string{"idToken": gameIDToken})
	req, _ := http.NewRequest(http.MethodPost, gameAuthAPI+"/sessions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sessions endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return parseSessionResponse(raw)
}

// parseSessionResponse extracts the sessionId from a /sessions response.
func parseSessionResponse(body []byte) (string, error) {
	var sr struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(body, &sr); err != nil {
		return "", err
	}
	if sr.SessionID == "" {
		return "", errors.New("sessions response missing sessionId")
	}
	return sr.SessionID, nil
}

// listAccounts returns the playable characters for a session.
func listAccounts(sessionID string) ([]Account, error) {
	req, _ := http.NewRequest(http.MethodGet, gameAuthAPI+"/accounts", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionID)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("accounts endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var accs []Account
	if err := json.Unmarshal(raw, &accs); err != nil {
		return nil, err
	}
	return accs, nil
}
