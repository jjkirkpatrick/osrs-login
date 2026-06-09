package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"
)

func TestPKCEChallengeMatchesVerifier(t *testing.T) {
	v, c := pkce()
	if v == "" || c == "" {
		t.Fatal("pkce returned empty strings")
	}
	if len(v) < 43 {
		t.Fatalf("verifier too short for RFC 7636: %d chars", len(v))
	}
	sum := sha256.Sum256([]byte(v))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if c != want {
		t.Fatalf("challenge %q != base64url(sha256(verifier)) %q", c, want)
	}
}

func TestLoginURLContainsExpectedParams(t *testing.T) {
	u, err := url.Parse(loginURL("CHAL", "STATE"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	cases := map[string]string{
		"response_type":         "code",
		"client_id":             launcherClientID,
		"redirect_uri":          launcherRedirect,
		"code_challenge":        "CHAL",
		"code_challenge_method": "S256",
		"scope":                 loginScope,
		"state":                 "STATE",
		"flow":                  "launcher",
	}
	for k, want := range cases {
		if got := q.Get(k); got != want {
			t.Errorf("login URL param %s = %q, want %q", k, got, want)
		}
	}
}

func TestParseAuthCode(t *testing.T) {
	got, err := parseAuthCode("https://secure.runescape.com/m=weblogin/launcher-redirect?code=abc123&state=xyz\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc123" {
		t.Fatalf("code = %q, want abc123", got)
	}
	if _, err := parseAuthCode("https://example.com/no-code-here"); err == nil {
		t.Fatal("expected an error when no code is present")
	}
}

func TestMergeQueryFragment(t *testing.T) {
	// Consent response: tokens arrive in the fragment.
	u, _ := url.Parse("http://localhost#id_token=GAME&code=C&state=S")
	if got := mergeQueryFragment(u).Get("id_token"); got != "GAME" {
		t.Fatalf("fragment id_token = %q, want GAME", got)
	}
	// Some providers use the query instead — handle both.
	u2, _ := url.Parse("http://localhost?id_token=QUERY")
	if got := mergeQueryFragment(u2).Get("id_token"); got != "QUERY" {
		t.Fatalf("query id_token = %q, want QUERY", got)
	}
}

func TestParseTokenResponse(t *testing.T) {
	body := []byte(`{"access_token":"a","id_token":"i","refresh_token":"r","expires_in":3600}`)
	tok, err := parseTokenResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "a" || tok.IDToken != "i" || tok.RefreshToken != "r" {
		t.Fatalf("unexpected tokens: %+v", tok)
	}
	if _, err := parseTokenResponse([]byte(`{"access_token":"a"}`)); err == nil {
		t.Fatal("expected error when id_token is missing")
	}
}

func TestParseSessionResponse(t *testing.T) {
	got, err := parseSessionResponse([]byte(`{"sessionId":"sess-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "sess-1" {
		t.Fatalf("sessionId = %q, want sess-1", got)
	}
	if _, err := parseSessionResponse([]byte(`{}`)); err == nil {
		t.Fatal("expected error when sessionId is missing")
	}
}

func TestAccountUnmarshal(t *testing.T) {
	var accs []Account
	if err := json.Unmarshal([]byte(`[{"accountId":"id1","displayName":"Zezima","userHash":"h"}]`), &accs); err != nil {
		t.Fatal(err)
	}
	if len(accs) != 1 || accs[0].DisplayName != "Zezima" || accs[0].AccountID != "id1" {
		t.Fatalf("unexpected accounts: %+v", accs)
	}
}
