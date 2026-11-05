package menus

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateCreateBoundsAndText(t *testing.T) {
	valid := CreateInput{ID: "menu", OwnerID: "owner", Title: "Dinner", Note: strings.Repeat("😀", 4000), CreatorName: strings.Repeat("😀", 80), Now: "now"}
	if err := ValidateCreate(valid); err != nil {
		t.Fatalf("valid maximum fields rejected: %v", err)
	}
	for name, mutate := range map[string]func(*CreateInput){
		"empty title":      func(v *CreateInput) { v.Title = " \t" },
		"title rune limit": func(v *CreateInput) { v.Title = strings.Repeat("x", 161) },
		"title byte limit": func(v *CreateInput) { v.Title = strings.Repeat("é", 321) },
		"note rune limit":  func(v *CreateInput) { v.Note = strings.Repeat("x", 4001) },
		"embedded nul":     func(v *CreateInput) { v.Note = "bad\x00note" },
		"invalid utf8":     func(v *CreateInput) { v.Note = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if !errors.Is(ValidateCreate(input), ErrInvalid) {
				t.Fatalf("invalid input accepted: %+v", input)
			}
		})
	}
}

func TestValidateEntryRequiresPairedLocalIdentityAndBounds(t *testing.T) {
	valid := EntryInput{ID: "entry", MenuID: "menu", LocalInstanceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LocalRecipeID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Label: strings.Repeat("😀", 80), Note: strings.Repeat("😀", 2000), ServingNote: strings.Repeat("😀", 160)}
	if err := ValidateEntry(valid); err != nil {
		t.Fatalf("valid maximum entry rejected: %v", err)
	}
	valid.LocalInstanceID = ""
	if !errors.Is(ValidateEntry(valid), ErrInvalid) {
		t.Fatal("half populated target accepted")
	}
	valid.LocalRecipeID = ""
	if err := ValidateEntry(valid); err != nil {
		t.Fatalf("paired null target rejected: %v", err)
	}
	valid.Label = strings.Repeat("x", 81)
	if !errors.Is(ValidateEntry(valid), ErrInvalid) {
		t.Fatal("oversized label accepted")
	}
}

func TestNormalizeTitleTrimsOuterWhitespace(t *testing.T) {
	if got := NormalizeTitle("  Supper  "); got != "Supper" {
		t.Fatalf("NormalizeTitle()=%q", got)
	}
}
