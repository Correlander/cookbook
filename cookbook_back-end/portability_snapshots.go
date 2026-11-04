package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"Cookbook_Database_Backend/internal/federation/exchangev2"
)

const (
	portableFormatV8               = "cookbook-export-v8"
	portableSnapshotEntryPrefix    = "snapshots/"
	portableSnapshotEntryVersion   = 1
	portableSnapshotClaimMagic     = "cookbook-private-snapshot-claim-v1\x00"
	maxPortableSnapshotEntries     = maxRecipeBookmarksPerAccount
	maxPortableSnapshotEntryBytes  = 16 << 20
	portableSnapshotPreserveHeader = "X-Cookbook-Preserve-Personal-Snapshots"
)

type portableSnapshotReference struct {
	BookmarkIndex int    `json:"bookmark_index"`
	Entry         string `json:"entry"`
	SHA256        string `json:"sha256"`
}

// portableSnapshotEntry is an archive DTO. It deliberately omits local trust
// state and stores evidence as one opaque claim byte string.
type portableSnapshotEntry struct {
	Version         int     `json:"version"`
	SourceOrigin    string  `json:"source_origin"`
	SourceRecipe    string  `json:"source_recipe_id"`
	SourceSequence  *int64  `json:"source_sequence,omitempty"`
	SourceEventID   *string `json:"source_event_id,omitempty"`
	SourceEventType *string `json:"source_event_type,omitempty"`
	CreatorName     string  `json:"creator_name"`
	ReceivedAt      string  `json:"received_at"`
	Payload         []byte  `json:"payload"`
	PayloadSHA256   []byte  `json:"payload_sha256"`
	Claim           []byte  `json:"untrusted_claim"`
}

func encodePortableSnapshotClaimV1(snapshot remoteSnapshotVersion) ([]byte, error) {
	if snapshot.Provenance != "publisher_verified" {
		return nil, errors.New("portable snapshot claim requires publisher capture")
	}
	evidence := snapshot.Evidence
	if len(evidence.ReceiptID) == 0 || len(evidence.EventBody) == 0 || len(evidence.EventBodyDigest) != sha256.Size || len(evidence.PublicKey) != 32 {
		return nil, errRemoteSnapshotEvidence
	}
	var output strings.Builder
	output.Grow(512 + len(evidence.EventBody))
	output.WriteString(portableSnapshotClaimMagic)
	fields := [][]byte{
		evidence.ReceiptID, evidence.EventBody, evidence.EventBodyDigest,
		[]byte(evidence.SignatureInput), []byte(evidence.SignatureValue),
		[]byte(evidence.RequestMethod), []byte(evidence.RequestTarget),
		[]byte(evidence.CoveredComponents), []byte(evidence.CoveredValues),
		[]byte(evidence.ContentDigest), []byte(evidence.AuthenticatedOrigin),
		[]byte(evidence.KeyID), []byte(evidence.PublicKey), []byte(evidence.KeyStatus),
		[]byte(evidence.KeyCreatedAt), []byte(evidence.Profile), []byte(evidence.AcceptedAt),
		[]byte(snapshot.GrantPurpose), []byte(snapshot.GrantRetention), []byte(snapshot.GrantPortability),
	}
	for _, field := range fields {
		if int64(len(field)) > maxRemoteSnapshotCaptureBytes || uint64(len(field)) > uint64(^uint32(0)) {
			return nil, errRemoteSnapshotTooLarge
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(field)))
		output.Write(size[:])
		output.Write(field)
	}
	var evidenceBytes [8]byte
	binary.BigEndian.PutUint64(evidenceBytes[:], uint64(evidence.EvidenceBytes))
	output.Write(evidenceBytes[:])
	return []byte(output.String()), nil
}

func portableSnapshotEntryFromVersion(snapshot remoteSnapshotVersion, bookmark portableBookmark) (portableSnapshotEntry, error) {
	entry := portableSnapshotEntry{Version: portableSnapshotEntryVersion, SourceOrigin: snapshot.SourceOrigin,
		SourceRecipe: snapshot.SourceRecipeID, CreatorName: snapshot.CreatorName, ReceivedAt: snapshot.ReceivedAt,
		Payload: append([]byte(nil), snapshot.Payload...), PayloadSHA256: append([]byte(nil), snapshot.PayloadDigest...)}
	if bookmark.TargetKind != "remote" || bookmark.RemoteObjectURI != snapshot.ObjectURI {
		return portableSnapshotEntry{}, errRemoteSnapshotEvidence
	}
	if snapshot.Sequence.Valid {
		value := snapshot.Sequence.Int64
		entry.SourceSequence = &value
	}
	if snapshot.EventID.Valid {
		value := snapshot.EventID.String
		entry.SourceEventID = &value
	}
	if snapshot.EventType.Valid {
		value := snapshot.EventType.String
		entry.SourceEventType = &value
	}
	switch snapshot.Provenance {
	case "publisher_verified":
		claim, err := encodePortableSnapshotClaimV1(snapshot)
		if err != nil {
			return portableSnapshotEntry{}, err
		}
		entry.Claim = claim
	case "user_imported_unverified":
		if len(snapshot.UntrustedClaim) == 0 {
			return portableSnapshotEntry{}, errRemoteSnapshotEvidence
		}
		entry.Claim = append([]byte(nil), snapshot.UntrustedClaim...)
	default:
		return portableSnapshotEntry{}, errRemoteSnapshotEvidence
	}
	if _, err := validatePortableSnapshotEntry(entry, bookmark); err != nil {
		return portableSnapshotEntry{}, err
	}
	return entry, nil
}

func validatePortableSnapshotEntry(entry portableSnapshotEntry, bookmark portableBookmark) (int64, error) {
	if entry.Version != portableSnapshotEntryVersion || bookmark.TargetKind != "remote" || bookmark.RemoteObjectURI == "" ||
		!utf8PortableString(entry.SourceOrigin, 2048) || !utf8PortableString(entry.SourceRecipe, 512) ||
		!utf8PortableString(entry.CreatorName, 200) || strings.TrimSpace(entry.CreatorName) == "" ||
		!utf8PortableString(entry.ReceivedAt, 64) || len(entry.Payload) == 0 || len(entry.Payload) > exchangev2.MaxPayloadBytes ||
		len(entry.Claim) == 0 || len(entry.Claim) > int(maxRemoteSnapshotCaptureBytes) {
		return 0, errors.New("invalid portable snapshot entry")
	}
	origin, recipeID, err := parseFederationRecipeObjectURI(bookmark.RemoteObjectURI)
	if err != nil || origin != entry.SourceOrigin || recipeID != entry.SourceRecipe {
		return 0, errors.New("portable snapshot source does not match its bookmark")
	}
	if entry.SourceSequence != nil && *entry.SourceSequence <= 0 {
		return 0, errors.New("invalid portable snapshot sequence")
	}
	if entry.SourceEventID != nil && (!utf8PortableString(*entry.SourceEventID, 200) || *entry.SourceEventID == "") {
		return 0, errors.New("invalid portable snapshot event ID")
	}
	if entry.SourceEventType != nil && (!utf8PortableString(*entry.SourceEventType, 200) || *entry.SourceEventType == "") {
		return 0, errors.New("invalid portable snapshot event type")
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.ReceivedAt); err != nil {
		return 0, errors.New("invalid portable snapshot received time")
	}
	digest := sha256.Sum256(entry.Payload)
	if len(entry.PayloadSHA256) != sha256.Size || !equalBytes(entry.PayloadSHA256, digest[:]) {
		return 0, errors.New("portable snapshot payload digest mismatch")
	}
	content, err := parseRemoteSnapshotPayload(entry.Payload)
	if err != nil {
		return 0, errRemoteSnapshotEvidence
	}
	encoded, err := portableSnapshotCaptureBytes(bookmark, entry, content)
	if err != nil {
		return 0, err
	}
	if encoded > maxRemoteSnapshotCaptureBytes {
		return 0, errRemoteSnapshotTooLarge
	}
	return encoded, nil
}

// Keep this formula aligned with persistUserImportedUnverifiedSnapshot's
// encoded_capture_bytes calculation in Core.
func portableSnapshotCaptureBytes(bookmark portableBookmark, entry portableSnapshotEntry, content RemoteRecipeContent) (int64, error) {
	origin, recipeID, err := parseFederationRecipeObjectURI(bookmark.RemoteObjectURI)
	if err != nil {
		return 0, err
	}
	tags, err := jsonMarshalCanonicalTags(content.Tags)
	if err != nil {
		return 0, err
	}
	return int64(len(bookmark.RemoteObjectURI) + len(origin) + len(recipeID) + len(entry.CreatorName) + len(entry.Payload) + len(entry.Claim) + len(entry.ReceivedAt) + len(content.Title) + len(content.Description) + len(tags) + 8*3), nil
}

func utf8PortableString(value string, maxBytes int) bool {
	return len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func snapshotEntryName(index int) string {
	return fmt.Sprintf("%s%08d.json", portableSnapshotEntryPrefix, index+1)
}

func marshalPortableSnapshotEntry(entry portableSnapshotEntry) ([]byte, error) {
	return json.Marshal(entry)
}

func decodePortableSnapshotEntry(raw []byte, bookmark portableBookmark) (portableSnapshotEntry, int64, error) {
	var entry portableSnapshotEntry
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entry); err != nil {
		return portableSnapshotEntry{}, 0, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return portableSnapshotEntry{}, 0, errors.New("trailing snapshot entry data")
	}
	decodedBytes, err := validatePortableSnapshotEntry(entry, bookmark)
	return entry, decodedBytes, err
}

func validatePortableSnapshotReferences(bundle portableBundle, files map[string][]byte) (int64, error) {
	if bundle.SnapshotEntries == nil {
		if len(files) != 0 {
			return 0, errors.New("snapshot entry is not referenced by the manifest")
		}
		return 0, nil
	}
	if !portableFormatHasSnapshots(bundle.Format) || len(*bundle.SnapshotEntries) > maxPortableSnapshotEntries || len(*bundle.SnapshotEntries) != len(files) {
		return 0, errors.New("invalid portable snapshot entry references")
	}
	seenIndexes := make(map[int]struct{}, len(*bundle.SnapshotEntries))
	seenEntries := make(map[string]struct{}, len(*bundle.SnapshotEntries))
	seenBookmarks := make(map[portableBookmarkIdentity]struct{}, len(bundle.Bookmarks))
	for _, bookmark := range bundle.Bookmarks {
		key := portableBookmarkKey(bookmark)
		if _, exists := seenBookmarks[key]; exists {
			return 0, errors.New("duplicate portable bookmark identity")
		}
		seenBookmarks[key] = struct{}{}
	}
	var decodedTotal int64
	for _, ref := range *bundle.SnapshotEntries {
		if ref.BookmarkIndex < 0 || ref.BookmarkIndex >= len(bundle.Bookmarks) || ref.Entry != snapshotEntryName(ref.BookmarkIndex) || len(ref.SHA256) != 64 {
			return 0, errors.New("invalid portable snapshot entry reference")
		}
		if _, ok := seenIndexes[ref.BookmarkIndex]; ok {
			return 0, errors.New("duplicate portable snapshot bookmark reference")
		}
		if _, ok := seenEntries[ref.Entry]; ok {
			return 0, errors.New("duplicate portable snapshot entry path")
		}
		seenIndexes[ref.BookmarkIndex] = struct{}{}
		seenEntries[ref.Entry] = struct{}{}
		raw, ok := files[ref.Entry]
		if !ok {
			return 0, errors.New("portable snapshot entry is missing")
		}
		expected, err := hex.DecodeString(ref.SHA256)
		if err != nil || len(expected) != sha256.Size {
			return 0, errors.New("invalid portable snapshot entry digest")
		}
		actual := sha256.Sum256(raw)
		if !equalBytes(expected, actual[:]) {
			return 0, errors.New("portable snapshot entry digest mismatch")
		}
		entry, decodedBytes, err := decodePortableSnapshotEntry(raw, bundle.Bookmarks[ref.BookmarkIndex])
		if err != nil {
			return 0, err
		}
		nextDecodedTotal, withinLimit := accumulatePortableArchiveSize(uint64(decodedTotal), uint64(portableSnapshotEntryLogicalBytes(entry)), uint64(maxImportBytes))
		if !withinLimit {
			return 0, errors.New("portable snapshot decoded data exceeds archive limit")
		}
		decodedTotal = int64(nextDecodedTotal)
		_ = decodedBytes
	}
	for name := range files {
		if _, ok := seenEntries[name]; !ok {
			return 0, errors.New("unreferenced portable snapshot entry")
		}
	}
	return decodedTotal, nil
}

func decodePortableSnapshotReferences(bundle portableBundle) (map[int]portableSnapshotEntry, error) {
	decoded := make(map[int]portableSnapshotEntry)
	if bundle.SnapshotEntries == nil {
		return decoded, nil
	}
	if _, err := validatePortableSnapshotReferences(bundle, bundle.snapshotFiles); err != nil {
		return nil, err
	}
	for _, ref := range *bundle.SnapshotEntries {
		entry, _, err := decodePortableSnapshotEntry(bundle.snapshotFiles[ref.Entry], bundle.Bookmarks[ref.BookmarkIndex])
		if err != nil {
			return nil, err
		}
		decoded[ref.BookmarkIndex] = entry
	}
	return decoded, nil
}

func writePortableSnapshotImportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errRemoteSnapshotAlreadyPinned):
		writeError(w, http.StatusConflict, "snapshot_already_pinned")
	case errors.Is(err, errRemoteSnapshotNotFound):
		writeError(w, http.StatusConflict, "snapshot_bookmark_conflict")
	case errors.Is(err, errRemoteSnapshotTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "snapshot_capture_too_large")
	case errors.Is(err, errRemoteSnapshotAccountCap):
		writeError(w, http.StatusConflict, "snapshot_limit_reached")
	case errors.Is(err, errRemoteSnapshotStoreCap):
		writeError(w, http.StatusConflict, "snapshot_storage_limit_reached")
	case errors.Is(err, errRemoteSnapshotEvidence):
		writeError(w, http.StatusBadRequest, "invalid_import")
	default:
		writeError(w, http.StatusInternalServerError, "storage_error")
	}
}

func validatePortableSnapshotForImport(ctx context.Context, tx *sql.Tx, accountID, bookmarkID string, bookmark portableBookmark, entry portableSnapshotEntry) (remoteSnapshotVersion, error) {
	if bookmark.TargetKind != "remote" {
		return remoteSnapshotVersion{}, errors.New("snapshot bookmark is not remote")
	}
	return persistUserImportedUnverifiedSnapshot(ctx, tx, accountID, bookmarkID,
		nullablePortableSequence(entry.SourceSequence), nullablePortableSnapshotFact(entry.SourceEventID),
		nullablePortableSnapshotFact(entry.SourceEventType), entry.CreatorName, entry.Payload, entry.Claim, entry.ReceivedAt)
}

func nullablePortableSequence(value *int64) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *value, Valid: true}
}

func nullablePortableSnapshotFact(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func portableSnapshotEntryLogicalBytes(entry portableSnapshotEntry) int64 {
	return int64(len(entry.Payload) + len(entry.Claim))
}

func portableSnapshotEntryDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func collectPortableSnapshots(ctx context.Context, db *sql.DB, accountID string, bookmarks []portableBookmark, bookmarkIDs []string) ([]portableSnapshotReference, map[string][]byte, error) {
	if len(bookmarks) != len(bookmarkIDs) {
		return nil, nil, errors.New("portable bookmark identity map is inconsistent")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	refs := make([]portableSnapshotReference, 0)
	files := make(map[string][]byte)
	var totalDecoded int64
	for index, bookmark := range bookmarks {
		if bookmark.TargetKind != "remote" {
			continue
		}
		snapshot, err := readOwnedActiveSnapshotForExport(ctx, tx, accountID, bookmarkIDs[index])
		if errors.Is(err, errRemoteSnapshotNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		entry, err := portableSnapshotEntryFromVersion(snapshot, bookmark)
		if err != nil {
			return nil, nil, err
		}
		raw, err := marshalPortableSnapshotEntry(entry)
		if err != nil {
			return nil, nil, err
		}
		name := snapshotEntryName(index)
		refs = append(refs, portableSnapshotReference{BookmarkIndex: index, Entry: name, SHA256: portableSnapshotEntryDigest(raw)})
		files[name] = raw
		totalDecoded += portableSnapshotEntryLogicalBytes(entry)
		if totalDecoded > maxImportBytes || len(refs) > maxPortableSnapshotEntries {
			return nil, nil, errPortableExportTooLarge
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return refs, files, nil
}

type boundedArchiveBuffer struct {
	buffer bytes.Buffer
	limit  int64
}

func (b *boundedArchiveBuffer) Write(data []byte) (int, error) {
	if int64(b.buffer.Len())+int64(len(data)) > b.limit {
		return 0, errPortableExportTooLarge
	}
	return b.buffer.Write(data)
}

func buildPortableExportArchive(manifest []byte, mediaDir string, mediaNames []string, snapshotFiles map[string][]byte) ([]byte, error) {
	return buildPortableExportArchiveWithLimits(manifest, mediaDir, mediaNames, snapshotFiles, maxImportBytes, maxImportManifestBytes, maxImportBytes)
}

func buildPortableExportArchiveWithLimits(manifest []byte, mediaDir string, mediaNames []string, snapshotFiles map[string][]byte, archiveLimit, manifestLimit, decodedLimit int64) ([]byte, error) {
	if len(manifest) == 0 || int64(len(manifest)) > manifestLimit || archiveLimit < 1 || decodedLimit < 1 {
		return nil, errPortableExportTooLarge
	}
	var decodedSize int64 = int64(len(manifest))
	mediaSizes := make(map[string]int64, len(mediaNames))
	for _, name := range mediaNames {
		info, err := os.Stat(filepath.Join(mediaDir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxRecipeImageBytes {
			return nil, errors.New("portable export media is unavailable")
		}
		mediaSizes[name] = info.Size()
		decodedSize += info.Size()
	}
	for name, data := range snapshotFiles {
		if !validPortableSnapshotPath(name) || len(data) == 0 {
			return nil, errors.New("invalid portable snapshot archive entry")
		}
		decodedSize += int64(len(data))
	}
	if decodedSize > decodedLimit {
		return nil, errPortableExportTooLarge
	}
	output := &boundedArchiveBuffer{limit: archiveLimit}
	archive := zip.NewWriter(output)
	manifestFile, err := archive.Create("manifest.json")
	if err == nil {
		_, err = manifestFile.Write(manifest)
	}
	if err == nil {
		sort.Strings(mediaNames)
		for _, name := range mediaNames {
			var entry io.Writer
			entry, err = archive.Create("media/" + name)
			if err != nil {
				break
			}
			var file *os.File
			file, err = os.Open(filepath.Join(mediaDir, name))
			if err != nil {
				break
			}
			var copied int64
			copied, err = io.Copy(entry, io.LimitReader(file, mediaSizes[name]+1))
			closeErr := file.Close()
			if err == nil && copied != mediaSizes[name] {
				err = errors.New("portable export media changed while staging")
			}
			if err == nil && closeErr != nil {
				err = closeErr
			}
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		names := make([]string, 0, len(snapshotFiles))
		for name := range snapshotFiles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			var entry io.Writer
			entry, err = archive.Create(name)
			if err == nil {
				_, err = entry.Write(snapshotFiles[name])
			}
			if err != nil {
				break
			}
		}
	}
	closeErr := archive.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if int64(output.buffer.Len()) > archiveLimit {
		return nil, errPortableExportTooLarge
	}
	return output.buffer.Bytes(), nil
}

func accumulatePortableArchiveSize(total, size, limit uint64) (uint64, bool) {
	if total > limit || size > limit-total {
		return total, false
	}
	return total + size, true
}

func validPortableSnapshotPath(name string) bool {
	if !strings.HasPrefix(name, portableSnapshotEntryPrefix) || !strings.HasSuffix(name, ".json") {
		return false
	}
	base := strings.TrimSuffix(strings.TrimPrefix(name, portableSnapshotEntryPrefix), ".json")
	if len(base) != 8 {
		return false
	}
	for _, char := range base {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
