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
	got, err := parseAuthCode("https://secure.runescape.com/m=weblogin/launcher-redirect?code=abc123&state=xyz\n", "xyz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc123" {
		t.Fatalf("code = %q, want abc123", got)
	}
	if _, err := parseAuthCode("https://x/?code=abc123&state=evil", "xyz"); err == nil {
		t.Fatal("expected an error on state mismatch")
	}
	if _, err := parseAuthCode("https://example.com/no-code-here", ""); err == nil {
		t.Fatal("expected an error when no code is present")
	}
}

// fakeJWT builds an unsigned JWT with the given payload claims (for tests only).
func fakeJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"none"}`))
	payload, _ := json.Marshal(claims)
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestTokenFromConsentRedirect(t *testing.T) {
	tok := fakeJWT(map[string]any{"nonce": "NONCE", "sub": "user"})
	u := "http://localhost/#id_token=" + tok + "&code=C&state=STATE"

	got, err := tokenFromConsentRedirect(u, "STATE", "NONCE")
	if err != nil {
		t.Fatal(err)
	}
	if got != tok {
		t.Fatalf("id_token = %q, want the JWT", got)
	}
	if _, err := tokenFromConsentRedirect(u, "WRONG", "NONCE"); err == nil {
		t.Fatal("expected an error on state mismatch")
	}
	if _, err := tokenFromConsentRedirect(u, "STATE", "WRONG"); err == nil {
		t.Fatal("expected an error on nonce mismatch")
	}
}

func TestChooseAccount(t *testing.T) {
	accs := []Account{
		{AccountID: "a1", DisplayName: "Alice"},
		{AccountID: "a2", DisplayName: "Bob"},
	}

	// explicit --character, case-insensitive
	if got, err := chooseAccount(accs, "bob", false, &Creds{}); err != nil || got.AccountID != "a2" {
		t.Fatalf("by name: got %+v, err %v", got, err)
	}
	// unknown --character errors
	if _, err := chooseAccount(accs, "zzz", false, &Creds{}); err == nil {
		t.Fatal("expected error for unknown character")
	}
	// single account needs no prompt
	if got, err := chooseAccount(accs[:1], "", false, &Creds{}); err != nil || got.AccountID != "a1" {
		t.Fatalf("single: got %+v, err %v", got, err)
	}
	// remembered account is used when not forcing a pick
	if got, err := chooseAccount(accs, "", false, &Creds{AccountID: "a2"}); err != nil || got.AccountID != "a2" {
		t.Fatalf("remembered: got %+v, err %v", got, err)
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
