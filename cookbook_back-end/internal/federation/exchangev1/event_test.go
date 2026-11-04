package exchangev1

import (
	"bytes"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEncodeDecodeExchangeV1Golden(t *testing.T) {
	event := Event{Protocol: "cookbook-exchange", SchemaVersion: 1, EventID: "0123456789abcdef0123456789abcdef", EventType: EventSnapshot, SourceOrigin: "https://cookbook.example.org", ObjectID: "https://cookbook.example.org/federation/recipes/0123456789abcdef0123456789abcdef", Sequence: 1, OccurredAt: "2026-10-31T12:00:00Z", Attribution: &Attribution{CreatorName: "Ada"}, Recipe: &Recipe{Title: "Soup", Description: "", Ingredients: []Ingredient{}, Steps: []Step{}, Servings: 1, Tags: []string{}}}
	body, err := Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{"protocol":"cookbook-exchange","schema_version":1,"event_id":"0123456789abcdef0123456789abcdef","event_type":"recipe.snapshot","source_origin":"https://cookbook.example.org","object_id":"https://cookbook.example.org/federation/recipes/0123456789abcdef0123456789abcdef","sequence":1,"occurred_at":"2026-10-31T12:00:00Z","attribution":{"creator_name":"Ada"},"recipe":{"title":"Soup","description":"","ingredients":[],"steps":[],"servings":1,"prep_time_minutes":0,"cook_time_minutes":0,"tags":[]}}`
	if string(body) != golden {
		t.Fatalf("wire bytes changed:\n%s", body)
	}
	decoded, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := Encode(decoded)
	if err != nil || !bytes.Equal(body, reencoded) {
		t.Fatalf("round trip: err=%v", err)
	}
}

func TestDecodeRejectsMalformedWire(t *testing.T) {
	valid, _ := Encode(Event{Protocol: "cookbook-exchange", SchemaVersion: 1})
	tests := map[string][]byte{
		"duplicate":        []byte(`{"protocol":"a","protocol":"b"}`),
		"nested duplicate": []byte(`{"recipe":{"title":"a","title":"b"}}`),
		"unknown":          []byte(`{"secret":1}`),
		"wrong case":       []byte(`{"Protocol":"x"}`),
		"trailing":         append(append([]byte(nil), valid...), []byte(` {}`)...),
		"invalid utf8":     append(append([]byte(nil), valid...), 0xff),
		"empty":            nil,
		"oversized":        bytes.Repeat([]byte(" "), MaxEventBytes+1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(body); err == nil {
				t.Fatal("accepted invalid wire")
			}
		})
	}
}

func TestProductionImportsStayWireOnly(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"bytes": true, "encoding/json": true, "errors": true, "io": true, "unicode/utf8": true}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !allowed[path] {
				t.Errorf("wire package production import %q is outside allowlist", path)
			}
		}
	}
}
