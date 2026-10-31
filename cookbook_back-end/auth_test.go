package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOpeningMagicLinkDoesNotConsumeEmailChallenge(t *testing.T) {
	db := testDatabase(t)
	token := strings.Repeat("a", 64)
	hash := sha256.Sum256([]byte(token))
	if _, err := db.Exec(`INSERT INTO email_challenges(id,email,token_hash,expires_at,created_at) VALUES('challenge','cook@example.test',?,?,?)`, hex.EncodeToString(hash[:]), time.Now().Add(15*time.Minute).UTC().Format(time.RFC3339Nano), timestamp()); err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	opened := callAPI(handler, http.MethodGet, "/auth/confirm?token="+token, nil, nil)
	if opened.Code != http.StatusOK {
		t.Fatalf("opening the confirmation page status=%d body=%s", opened.Code, opened.Body)
	}
	if got := opened.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("confirmation page cache-control=%q, want no-store", got)
	}
	var consumed *string
	if err := db.QueryRow(`SELECT consumed_at FROM email_challenges WHERE id='challenge'`).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatalf("opening the email link consumed its challenge at %s", *consumed)
	}
}

func TestInvitationLinkPageIsNotCacheable(t *testing.T) {
	db := testDatabase(t)
	recorder := callAPI(handlerWithAuth(db, testAuth(db)), http.MethodGet, "/?invite=one-time-token", nil, nil)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invitation page status=%d cache-control=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
}

func TestAuthConfigBuildsWebAuthnRelyingPartyFromInstanceURL(t *testing.T) {
	db := testDatabase(t)
	t.Setenv("COOKBOOK_PUBLIC_URL", "https://Recipes.Example.test:443/")
	t.Setenv("COOKBOOK_RP_ID", "recipes.example.test")
	t.Setenv("COOKBOOK_RP_ORIGIN", "https://recipes.example.test")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "/bin/true")
	config, err := authConfig(db)
	if err != nil {
		t.Fatal(err)
	}
	if !config.secureCookies || config.webauthn.Config.RPID != "recipes.example.test" || len(config.webauthn.Config.RPOrigins) != 1 || config.webauthn.Config.RPOrigins[0] != "https://recipes.example.test" {
		t.Fatalf("unexpected WebAuthn relying party config: secure=%v config=%#v", config.secureCookies, config.webauthn.Config)
	}
	t.Setenv("COOKBOOK_RP_ORIGIN", "https://other.example.test")
	if _, err := authConfig(db); err == nil {
		t.Fatal("mismatched WebAuthn and public origins should be rejected")
	}
}

func TestAuthConfigRequiresValidSMTPConfigurationOrCommand(t *testing.T) {
	db := testDatabase(t)
	t.Setenv("COOKBOOK_PUBLIC_URL", "https://recipes.example.test")
	t.Setenv("COOKBOOK_RP_ID", "recipes.example.test")
	t.Setenv("COOKBOOK_RP_ORIGIN", "https://recipes.example.test")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "")
	t.Setenv("COOKBOOK_SMTP_HOST", "mail.example.test:587")
	t.Setenv("COOKBOOK_SMTP_FROM", "cookbook@example.test")
	t.Setenv("COOKBOOK_SMTP_USERNAME", "mailer")
	t.Setenv("COOKBOOK_SMTP_PASSWORD", "")
	if _, err := authConfig(db); err == nil {
		t.Fatal("partial SMTP credentials should be rejected")
	}
	t.Setenv("COOKBOOK_SMTP_PASSWORD", "secret")
	handler, err := authConfig(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := handler.sender.(smtpEmailSender); !ok {
		t.Fatalf("sender type = %T, want SMTP", handler.sender)
	}
}

func TestCommandEmailSenderKeepsSignInURLOutOfArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a shell script")
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "payload.json")
	script := filepath.Join(dir, "capture-mail")
	body := "#!/bin/sh\nprintf '%s' \"$*\" > \"$CAPTURE_ARGS\"\ncat > \"$CAPTURE_BODY\"\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_ARGS", filepath.Join(dir, "args.txt"))
	t.Setenv("CAPTURE_BODY", output)
	link := "https://cookbook.example/auth/confirm?token=secret-once"
	if err := (commandEmailSender{command: script}).SendLoginLink("cook@example.test", link); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "secret-once") {
		t.Fatalf("sign-in token leaked in command args: %q", args)
	}
	var payload loginEmail
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.To != "cook@example.test" || payload.SignInURL != link {
		t.Fatalf("unexpected email payload: %#v", payload)
	}
}
