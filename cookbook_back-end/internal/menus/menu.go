// Package menus contains the private meal-menu domain and SQL persistence.
// Cross-feature recipe and creator policy remains in the application root.
package menus

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MaxMenusPerOwner = 1000
	MaxEntries       = 64
)

var (
	ErrInvalid  = errors.New("invalid menu")
	ErrNotFound = errors.New("menu not found")
	ErrConflict = errors.New("menu version conflict")
	ErrLimit    = errors.New("menu limit reached")
	ErrTarget   = errors.New("recipe unavailable")
	ErrEntry    = errors.New("menu entry not found")
)

type Menu struct {
	ID          string
	OwnerID     string
	Title       string
	Note        string
	CreatorName string
	Version     int
	CreatedAt   string
	UpdatedAt   string
	Entries     []Entry
}

type Entry struct {
	ID              string
	MenuID          string
	Position        int
	LocalInstanceID string
	LocalRecipeID   string
	Label           string
	Note            string
	ServingNote     string
}

type CreateInput struct {
	ID, OwnerID, Title, Note, CreatorName, Now string
}

type UpdateInput struct {
	Title, Note, Now string
}

type EntryInput struct {
	ID, MenuID, LocalInstanceID, LocalRecipeID string
	Label, Note, ServingNote                   string
}

func ValidateCreate(in CreateInput) error {
	if !validText(in.Title, 160, 640, true) || !validText(in.Note, 4000, 16000, false) || !validText(in.CreatorName, 80, 320, false) || in.ID == "" || in.OwnerID == "" || in.Now == "" {
		return ErrInvalid
	}
	return nil
}

func ValidateUpdate(in UpdateInput) error {
	if !validText(in.Title, 160, 640, true) || !validText(in.Note, 4000, 16000, false) || in.Now == "" {
		return ErrInvalid
	}
	return nil
}

func ValidateEntry(in EntryInput) error {
	if in.ID == "" || in.MenuID == "" || ValidateEntryFields(in.Label, in.Note, in.ServingNote) != nil {
		return ErrInvalid
	}
	if (in.LocalInstanceID == "") != (in.LocalRecipeID == "") {
		return ErrInvalid
	}
	if in.LocalInstanceID != "" && (!validOpaqueID(in.LocalInstanceID) || !validOpaqueID(in.LocalRecipeID)) {
		return ErrInvalid
	}
	return nil
}

// ValidateEntryFields applies the bounded owner-authored text rules before a
// caller performs cross-feature reads such as recipe authorization.
func ValidateEntryFields(label, note, servingNote string) error {
	if !validText(label, 80, 320, false) || !validText(note, 2000, 8000, false) || !validText(servingNote, 160, 640, false) {
		return ErrInvalid
	}
	return nil
}

func NormalizeTitle(value string) string { return strings.TrimSpace(value) }

func validText(value string, maxRunes, maxBytes int, required bool) bool {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || utf8.RuneCountInString(value) > maxRunes || len(value) > maxBytes {
		return false
	}
	return !required || (len(value) > 0 && strings.TrimSpace(value) != "")
}

func validOpaqueID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
