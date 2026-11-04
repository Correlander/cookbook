package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
)

const (
	snapshotBrowserFixtureOptIn = "COOKBOOK_E2E_PRIVATE_SNAPSHOT_FIXTURE"
	snapshotBrowserRunDir       = "COOKBOOK_E2E_RUN_DIR"
	snapshotBrowserInbox        = "COOKBOOK_E2E_INBOX_URL"
	snapshotBrowserReceiver     = "https://cookbook-e2e.example.org"
	snapshotBrowserSource       = "https://snapshot-source.example.org"
)

// This fixture is called only by the existing e2e runner after it creates an
// isolated database and starts the API. It never creates a bookmark or receipt
// row: each trusted receipt must come from a fresh signed inbox request.
func TestPreparePrivateSnapshotBrowserFixture(t *testing.T) {
	if os.Getenv(snapshotBrowserFixtureOptIn) != "1" {
		t.Skip("private snapshot browser fixture requires explicit runner opt-in")
	}
	runDir, databasePath, inboxURL, err := validateSnapshotBrowserFixtureEnvironment()
	if err != nil {
		t.Fatal(err)
	}

	keyDir := filepath.Join(runDir, "private-snapshot-fixture-key")
	key, err := loadOrCreateFederationSigningKey(keyDir)
	if err != nil {
		t.Fatal("create disposable federation signing key:", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(keyDir) })

	db, err := openDatabase(databasePath)
	if err != nil {
		t.Fatal("open runner database:", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := registerFederationPeer(db, snapshotBrowserSource, true); err != nil {
		t.Fatal("register synthetic federation peer:", err)
	}
	if err := registerFederationPeerKey(db, snapshotBrowserSource, key.ID, key.PublicKey); err != nil {
		t.Fatal("register synthetic peer key:", err)
	}
	if _, err := db.Exec(`UPDATE federation_peers SET exchange_profile=? WHERE origin=?`, federationExchangeV2Retention, snapshotBrowserSource); err != nil {
		t.Fatal("enable v2 for disposable synthetic peer:", err)
	}
	if err := assertSnapshotFixtureOrigin(db); err != nil {
		t.Fatal("validate runner federation origin:", err)
	}
	if _, err := db.Exec(`UPDATE instance_settings SET federation_enabled=1 WHERE id=1`); err != nil {
		t.Fatal("enable inbox in runner database:", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	for _, fixture := range []struct {
		id, title string
	}{
		{"10101010101010101010101010101010", "Snapshot journey soup"},
		{"20202020202020202020202020202020", "Explicit capture stew"},
		{"30303030303030303030303030303030", "Restricted snapshot tart"},
		{"40404040404040404040404040404040", "Removed snapshot bread"},
		{"50505050505050505050505050505050", "Removal snapshot rice"},
	} {
		if err := postSignedBrowserSnapshot(t, client, key, inboxURL, fixture.id, fixture.title); err != nil {
			t.Fatalf("deliver signed synthetic snapshot %s: %v", fixture.id, err)
		}
	}
}

func validateSnapshotBrowserFixtureEnvironment() (runDir, databasePath, inboxURL string, err error) {
	runDir = os.Getenv(snapshotBrowserRunDir)
	databasePath = strings.TrimSpace(os.Getenv("COOKBOOK_DATABASE"))
	inboxURL = strings.TrimSpace(os.Getenv(snapshotBrowserInbox))
	if runDir == "" || databasePath == "" || inboxURL == "" {
		return "", "", "", errors.New("runner-owned directory, database and inbox URL are required")
	}
	absRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return "", "", "", errors.New("invalid runner-owned directory")
	}
	rootInfo, err := os.Lstat(absRunDir)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm()&0077 != 0 || !strings.HasPrefix(filepath.Base(absRunDir), "cookbook-e2e-") {
		return "", "", "", errors.New("fixture directory must be an existing private directory")
	}
	if filepath.Base(databasePath) != "cookbook.db" {
		return "", "", "", errors.New("fixture database must use the runner's disposable database path")
	}
	dbAbs, err := filepath.Abs(databasePath)
	if err != nil {
		return "", "", "", errors.New("invalid fixture database path")
	}
	rel, err := filepath.Rel(absRunDir, dbAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", "", errors.New("fixture database must be inside the runner-owned directory")
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(absRunDir)
	resolvedDB, dbErr := filepath.EvalSymlinks(dbAbs)
	if rootErr != nil || dbErr != nil {
		return "", "", "", errors.New("fixture database and directory must already exist")
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedDB)
	if err != nil || resolvedRel == "." || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) || filepath.IsAbs(resolvedRel) {
		return "", "", "", errors.New("resolved fixture database escaped the runner-owned directory")
	}
	dbInfo, err := os.Lstat(dbAbs)
	if err != nil || !dbInfo.Mode().IsRegular() || dbInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", "", errors.New("fixture database must be an existing regular file")
	}
	target, err := url.Parse(inboxURL)
	if err != nil || target.Scheme != "http" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Path != "/api/federation/inbox" || target.Port() == "" {
		return "", "", "", errors.New("fixture inbox must be an explicit loopback HTTP API URL")
	}
	host := strings.TrimSuffix(strings.ToLower(target.Hostname()), ".")
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", "", "", errors.New("fixture inbox host must be loopback")
	}
	configuredAddress := strings.TrimSpace(os.Getenv("COOKBOOK_ADDR"))
	configuredHost, configuredPort, addressErr := net.SplitHostPort(configuredAddress)
	if addressErr != nil || configuredHost != "127.0.0.1" || configuredPort == "" || target.Host != configuredAddress {
		return "", "", "", errors.New("fixture inbox must match the runner's configured API listener")
	}
	return absRunDir, resolvedDB, inboxURL, nil
}

func assertSnapshotFixtureOrigin(db *sql.DB) error {
	var origin string
	var enabled int
	if err := db.QueryRow(`SELECT federation_origin,federation_enabled FROM instance_settings WHERE id=1`).Scan(&origin, &enabled); err != nil {
		return err
	}
	canonical, err := canonicalFederationOrigin(origin)
	if err != nil || canonical != snapshotBrowserReceiver || enabled != 0 {
		return fmt.Errorf("expected disabled configured test origin %q before fixture, got %q (enabled=%d)", snapshotBrowserReceiver, origin, enabled)
	}
	return nil
}

func postSignedBrowserSnapshot(t *testing.T, client *http.Client, key federationSigningKey, inboxURL, recipeID, title string) error {
	t.Helper()
	objectID := snapshotBrowserSource + "/federation/recipes/" + recipeID
	source := federationRecipeSource{
		ID: recipeID, CreatorName: "Synthetic snapshot cook", Title: title,
		Description: "A test-only recipe delivered through the signed federation inbox.",
		Ingredients: []ingredient{{Name: "Tomato", Quantity: "2"}},
		Steps:       []recipeStep{{Text: "Simmer the synthetic ingredients."}},
		Servings:    2, PrepTime: 5, CookTime: 20,
		Tags: []string{"synthetic"}, Visibility: visibilityPublic,
	}
	eventID := newID()
	unsigned, err := serializeFederationV2Event(snapshotBrowserSource, eventID, exchangev2.RecipeSnapshot, 1, time.Now(), source, nil, nil)
	if err != nil {
		return fmt.Errorf("serialize unsigned event: %w", err)
	}
	payload := mustDecodeV2Payload(t, unsigned)
	grant := &exchangev2.RetentionGrant{
		Purpose: exchangev2.GrantPurpose, Retention: exchangev2.GrantRetention,
		Portability: exchangev2.GrantPortability, ObjectID: objectID, Sequence: 1,
		PayloadSHA256: exchangev2.PayloadDigest(payload),
	}
	eventBody, err := serializeFederationV2Event(snapshotBrowserSource, eventID, exchangev2.RecipeSnapshot, 1, time.Now(), source, payload, grant)
	if err != nil {
		return fmt.Errorf("serialize granted event: %w", err)
	}
	req := signedFederationRequestWithTagAtOrigin(t, key.PrivateKey, key.ID, newID()+newID(), string(eventBody), federationSignatureV2Tag, snapshotBrowserReceiver)
	loopback, err := url.Parse(inboxURL)
	if err != nil {
		return fmt.Errorf("parse validated loopback inbox: %w", err)
	}
	req.URL.Scheme = loopback.Scheme
	req.URL.Host = loopback.Host
	req.Host = strings.TrimPrefix(snapshotBrowserReceiver, "https://")
	req.RequestURI = ""
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post event to running API: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return fmt.Errorf("read inbox acknowledgement: %w", err)
	}
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("running API rejected synthetic signed event: status=%d body=%q", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
