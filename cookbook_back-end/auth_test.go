package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
