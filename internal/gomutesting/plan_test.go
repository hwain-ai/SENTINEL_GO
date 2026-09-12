package gomutesting

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const positiveSource = "package probe\n\nfunc Positive(value int) bool { return value > 0 }\n"

func TestDiscoverUsesPinnedExternalOperatorsWithoutChangingInput(t *testing.T) {
	source := []byte(positiveSource)
	sites, err := Discover("value.go", source)
	if err != nil || len(sites) != 3 {
		t.Fatalf("sites=%v err=%v", sites, err)
	}
	if !bytes.Equal(source, []byte(positiveSource)) {
		t.Fatal("discovery changed source")
	}
	seen := map[string]bool{}
	for _, site := range sites {
		if len(site.ID) != 64 || seen[site.ID] || site.SourceFile != "value.go" || site.Line != 3 {
			t.Fatalf("invalid candidate: %#v", site)
		}
		seen[site.ID] = true
		if bytes.Equal(site.replacement, source) {
			t.Fatal("external operator did not change source")
		}
	}
	again, err := Discover("value.go", source)
	if err != nil || sites[0].ID != again[0].ID {
		t.Fatal("candidate identity is not deterministic")
	}
	encoded, err := json.Marshal(sites)
	if err != nil || strings.Contains(string(encoded), "return") || strings.Contains(string(encoded), "replacement") {
		t.Fatal("candidate JSON leaks source")
	}
}

func TestDiscoverRejectsInvalidSourceAndPreservesEmptyInventory(t *testing.T) {
	for _, file := range []string{"", "../value.go", "/value.go", "value_test.go", "a/../value.go", "invalid\xff.go"} {
		if _, err := Discover(file, []byte(positiveSource)); err == nil {
			t.Fatalf("accepted path %q", file)
		}
	}
	if _, err := Discover("value.go", []byte("package !")); err == nil {
		t.Fatal("accepted malformed source")
	}
	sites, err := Discover("value.go", []byte("package probe\ntype Value struct{}\n"))
	if err != nil || len(sites) != 0 {
		t.Fatalf("empty inventory: sites=%v err=%v", sites, err)
	}
}

func TestCandidateIdentityIncludesSourceBytesAndIgnoresLineDirectives(t *testing.T) {
	a, err := Discover("value.go", []byte(positiveSource))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Discover("value.go", []byte("//line /private/source.go:900\n"+positiveSource))
	if err != nil {
		t.Fatal(err)
	}
	if a[0].ID == b[0].ID || b[0].SourceFile != "value.go" || b[0].Line != 4 {
		t.Fatal("source identity or physical location was lost")
	}
}

func TestSourceReadStopsAtSizeLimit(t *testing.T) {
	if _, err := boundedSource(strings.NewReader(strings.Repeat("x", maximumSourceBytes+1))); err == nil || err.Error() != "goMutestingSourceTooLarge" {
		t.Fatalf("err=%v", err)
	}
	payload, err := boundedSource(strings.NewReader(positiveSource))
	if err != nil || string(payload) != positiveSource {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
}
