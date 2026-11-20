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

	// userAgent is sent on every request; Jagex's edge rejects the default Go
	// user-agent with 403 on some endpoints.
	userAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
)

// httpClient follows redirects; noRedirect lets us read the Location header of
// the consent step instead of following it.
var (
	httpClient = &http.Client{Timeout: 30 * time.Second}
	noRedirect = &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

// send sets the shared User-Agent and performs the request.
func send(client *http.Client, req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", userAgent)
	return client.Do(req)
}

// httpError is a non-2xx response from a Jagex endpoint.
type httpError struct {
	endpoint string
	status   int
	body     string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("%s endpoint returned %d: %s", e.endpoint, e.status, e.body)
}

// rejected reports whether err is Jagex refusing our credentials (expired or
// revoked), as opposed to a network or server fault where a fresh login won't help.
func rejected(err error) bool {
	var he *httpError
	if !errors.As(err, &he) {
		return false
	}
	return he.status == http.StatusBadRequest || he.status == http.StatusUnauthorized || he.status == http.StatusForbidden
}

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

// consentURL builds the consent authorization URL. Opened in the browser it
// carries the login session cookie and redirects to http://localhost with a
// game-scoped id_token in the fragment. The caller supplies state and nonce so
// it can verify them on the way back.
func consentURL(idToken, state, nonce string) string {
	q := url.Values{
		"id_token_hint": {idToken},
		"nonce":         {nonce},
		"prompt":        {"consent"},
		"redirect_uri":  {consentRedirect},
		"response_type": {"id_token code"},
		"state":         {state},
		"client_id":     {consentClientID},
		"scope":         {consentScope},
	}
	return authOrigin + "/oauth2/auth?" + q.Encode()
}

// parseAuthCode extracts the `code` from the URL the user is redirected to after
// logging in, verifying the `state` round-trips to guard against CSRF. It accepts
// both the launcher-redirect https URL and the `jagex:code=…,state=…` URI that
// page hands to the registered scheme handler.
func parseAuthCode(redirectedURL, expectedState string) (string, error) {
	s := strings.TrimSpace(redirectedURL)
	var vals url.Values
	if rest, ok := strings.CutPrefix(s, "jagex:"); ok {
		v, err := url.ParseQuery(strings.ReplaceAll(rest, ",", "&"))
		if err != nil {
			return "", fmt.Errorf("malformed jagex: URI: %w", err)
		}
		vals = v
	} else {
		u, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("that doesn't look like a URL: %w", err)
		}
		vals = mergeQueryFragment(u)
	}
	if got := vals.Get("state"); got != expectedState {
		return "", fmt.Errorf("state mismatch (possible CSRF) — got %q, expected %q", got, expectedState)
	}
	code := vals.Get("code")
	if code == "" {
		return "", errors.New("no `code` found in the pasted URL — did you copy the full address after logging in?")
	}
	return code, nil
}

// tokenFromConsentRedirect extracts the game-scoped id_token from the
// http://localhost URL the consent step redirects to, verifying both the `state`
// parameter and the id_token's `nonce` round-trip.
func tokenFromConsentRedirect(pasted, expectedState, expectedNonce string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(pasted))
	if err != nil {
		return "", fmt.Errorf("that doesn't look like a URL: %w", err)
	}
	vals := mergeQueryFragment(u)
	if got := vals.Get("state"); got != expectedState {
		return "", fmt.Errorf("state mismatch (possible CSRF) — got %q, expected %q", got, expectedState)
	}
	t := vals.Get("id_token")
	if t == "" {
		return "", errors.New("no `id_token` found in the pasted URL")
	}
	if err := validateNonce(t, expectedNonce); err != nil {
		return "", err
	}
	return t, nil
}

// validateNonce checks that a JWT's `nonce` claim matches what we sent.
func validateNonce(idToken, expectedNonce string) error {
	claims, err := jwtClaims(idToken)
	if err != nil {
		return err
	}
	if got, _ := claims["nonce"].(string); got != expectedNonce {
		return errors.New("id_token nonce mismatch")
	}
	return nil
}

// jwtClaims decodes (without verifying the signature) the payload of a JWT.
func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT: %d sections, expected 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parsing JWT payload: %w", err)
	}
	return claims, nil
}

// mergeQueryFragment returns the union of a URL's query and fragment parameters.
// The consent step returns its result in the fragment; the login step returns
// `code` in the query.
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
	resp, err := send(httpClient, req)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return Tokens{}, &httpError{"token", resp.StatusCode, strings.TrimSpace(string(body))}
	}
	return parseTokenResponse(body)
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

// consentExchange tries the consent step server-side, reading the redirect
// Location header. This works only if Jagex accepts id_token_hint without a
// browser session; if not, the caller falls back to an interactive browser
// consent.
func consentExchange(idToken string) (string, error) {
	state, nonce := randomString(16), randomString(16)
	req, _ := http.NewRequest(http.MethodGet, consentURL(idToken, state, nonce), nil)
	resp, err := send(noRedirect, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("consent returned status %d without a redirect", resp.StatusCode)
	}
	return tokenFromConsentRedirect(loc, state, nonce)
}

// createSession exchanges an id_token for a game session id.
func createSession(idToken string) (string, error) {
	body, _ := json.Marshal(map[string]string{"idToken": idToken})
	req, _ := http.NewRequest(http.MethodPost, gameAuthAPI+"/sessions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := send(httpClient, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", &httpError{"sessions", resp.StatusCode, strings.TrimSpace(string(raw))}
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

// listAccounts returns the playable characters for a session. A non-200 comes
// back as an *httpError; see rejected() for telling an expired session apart.
func listAccounts(sessionID string) ([]Account, error) {
	req, _ := http.NewRequest(http.MethodGet, gameAuthAPI+"/accounts", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionID)
	resp, err := send(httpClient, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, &httpError{"accounts", resp.StatusCode, strings.TrimSpace(string(raw))}
	}
	var accs []Account
	if err := json.Unmarshal(raw, &accs); err != nil {
		return nil, err
	}
	return accs, nil
}
