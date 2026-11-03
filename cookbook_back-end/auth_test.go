package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
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

func TestCurrentUserResponseIncludesAuthoritativeAdminFlag(t *testing.T) {
	db := testDatabase(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,display_name,is_admin,created_at) VALUES
		('admin','admin@example.test','Admin',1,'now'),('member','member@example.test','Member',0,'now')`); err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := issueSession(db, "admin")
	if err != nil {
		t.Fatal(err)
	}
	memberToken, _, err := issueSession(db, "member")
	if err != nil {
		t.Fatal(err)
	}
	handler := handlerWithAuth(db, testAuth(db))
	type currentUser struct {
		ID          string `json:"id"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		IsAdmin     *bool  `json:"is_admin"`
	}
	check := func(token, id, email, name string, wantAdmin bool) {
		t.Helper()
		response := callAPI(handler, http.MethodGet, "/api/auth/me", nil, &http.Cookie{Name: sessionCookie, Value: token})
		var got currentUser
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil || got.ID != id || got.Email != email || got.DisplayName != name || got.IsAdmin == nil || *got.IsAdmin != wantAdmin {
			t.Fatalf("/api/auth/me status=%d body=%s decoded=%+v", response.Code, response.Body, got)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("/api/auth/me cache-control=%q, want no-store", response.Header().Get("Cache-Control"))
		}
	}
	check(adminToken, "admin", "admin@example.test", "Admin", true)
	check(memberToken, "member", "member@example.test", "Member", false)
	if _, err := db.Exec(`UPDATE users SET is_admin=0 WHERE id='admin'`); err != nil {
		t.Fatal(err)
	}
	check(adminToken, "admin", "admin@example.test", "Admin", false)
	denied := callAPI(handler, http.MethodGet, "/api/auth/me", nil, nil)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/auth/me status=%d body=%s", denied.Code, denied.Body)
	}
}

func TestAuthConfigBuildsWebAuthnRelyingPartyFromInstanceURL(t *testing.T) {
	db := testDatabase(t)
	t.Setenv("COOKBOOK_PUBLIC_URL", "https://Recipes.Example.test:443/")
	t.Setenv("COOKBOOK_RP_ID", "recipes.example.test")
	t.Setenv("COOKBOOK_RP_ORIGIN", "https://recipes.example.test")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "/bin/true")
	t.Setenv("COOKBOOK_SMTP_HOST", "")
	t.Setenv("COOKBOOK_SMTP_FROM", "")
	t.Setenv("COOKBOOK_SMTP_USERNAME", "")
	t.Setenv("COOKBOOK_SMTP_PASSWORD", "")
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

func TestAuthConfigSupportsDisabledEmailAndRejectsPartialSMTPConfiguration(t *testing.T) {
	db := testDatabase(t)
	t.Setenv("COOKBOOK_PUBLIC_URL", "http://localhost:5173")
	t.Setenv("COOKBOOK_RP_ID", "localhost")
	t.Setenv("COOKBOOK_RP_ORIGIN", "http://localhost:5173")
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "")
	t.Setenv("COOKBOOK_SMTP_HOST", "")
	t.Setenv("COOKBOOK_SMTP_FROM", "")
	t.Setenv("COOKBOOK_SMTP_USERNAME", "")
	t.Setenv("COOKBOOK_SMTP_PASSWORD", "")
	config, err := authConfig(db)
	if err != nil {
		t.Fatalf("no-mail auth configuration should start: %v", err)
	}
	if config.sender != nil {
		t.Fatalf("no-mail sender = %T, want nil", config.sender)
	}
	if config.webauthn.Config.RPID != "localhost" || len(config.webauthn.Config.RPOrigins) != 1 || config.webauthn.Config.RPOrigins[0] != "http://localhost:5173" {
		t.Fatalf("no-mail configuration lost WebAuthn relying party: %#v", config.webauthn.Config)
	}
	noMailHandler := handlerWithAuthConfig(db, config, configForAuthTest(t))
	loginBegin := callAPI(noMailHandler, http.MethodPost, "/api/auth/passkey/login/begin", nil, nil)
	if loginBegin.Code != http.StatusOK || !strings.Contains(loginBegin.Body.String(), `"ceremony_id"`) {
		t.Fatalf("passkey login begin without mail status=%d body=%s", loginBegin.Code, loginBegin.Body)
	}
	var rateLimitCountBefore, rateLimitCountAfter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_rate_limits`).Scan(&rateLimitCountBefore); err != nil {
		t.Fatal(err)
	}
	requestEmail := callAPI(noMailHandler, http.MethodPost, "/api/auth/email", map[string]string{"email": "cook@example.test"}, nil)
	if requestEmail.Code != http.StatusServiceUnavailable || requestEmail.Body.String() != "{\"error\":\"email_unavailable\"}\n" {
		t.Fatalf("email request without sender status=%d body=%q", requestEmail.Code, requestEmail.Body.String())
	}
	var challengeCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM email_challenges`).Scan(&challengeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_rate_limits`).Scan(&rateLimitCountAfter); err != nil {
		t.Fatal(err)
	}
	if challengeCount != 0 || rateLimitCountBefore != rateLimitCountAfter {
		t.Fatalf("unavailable email request changed auth state: challenges=%d rate_limits_before=%d rate_limits_after=%d", challengeCount, rateLimitCountBefore, rateLimitCountAfter)
	}

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
	t.Setenv("COOKBOOK_EMAIL_COMMAND", "/bin/true")
	if _, err := authConfig(db); err == nil {
		t.Fatal("command and SMTP configuration should not be combined")
	}
}

func configForAuthTest(t *testing.T) config {
	t.Helper()
	t.Setenv("COOKBOOK_DATABASE", filepath.Join(t.TempDir(), "cookbook.db"))
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return config
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

func TestCommandEmailSenderTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a shell script")
	}
	script := filepath.Join(t.TempDir(), "slow-mailer")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err := (commandEmailSender{command: script, timeout: 100 * time.Millisecond}).SendLoginLink("cook@example.test", "https://cookbook.example/auth/confirm?token=secret")
	if err == nil {
		t.Fatal("expected the timed out mail command to fail")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("mail command timeout took %s", elapsed)
	}
}

func TestSMTPSenderTimesOutWaitingForGreeting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var buffer [1]byte
		_, _ = conn.Read(buffer[:])
	}()

	started := time.Now()
	err = (smtpEmailSender{address: listener.Addr().String(), from: "cookbook@example.test", timeout: 100 * time.Millisecond}).SendLoginLink("cook@example.test", "https://cookbook.example/auth/confirm?token=secret")
	if err == nil {
		t.Fatal("expected SMTP greeting timeout to fail")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("SMTP greeting timeout took %s", elapsed)
	}
}
