package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
