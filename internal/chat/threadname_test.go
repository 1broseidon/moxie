package chat

import (
	"strings"
	"testing"
)

func TestNewThreadName_Basic(t *testing.T) {
	name := NewThreadName(nil)
	parts := strings.Split(name, "-")
	if len(parts) != 2 {
		t.Fatalf("expected two-word name, got %q", name)
	}
}

func TestNewThreadName_NoDuplicates(t *testing.T) {
	existing := make(map[string]bool)
	for i := 0; i < 100; i++ {
		name := NewThreadName(existing)
		if existing[name] {
			t.Fatalf("duplicate name generated: %s", name)
		}
		existing[name] = true
	}
}

func TestNewThreadName_FallsBackToThreeWords(t *testing.T) {
	// Fill all two-word combos
	existing := make(map[string]bool)
	for _, a := range adjectives {
		for _, n := range nouns {
			existing[a+"-"+n] = true
		}
	}
	name := NewThreadName(existing)
	parts := strings.Split(name, "-")
	if len(parts) != 3 {
		t.Fatalf("expected three-word name after exhaustion, got %q", name)
	}
	if existing[name] {
		t.Fatalf("three-word name collided: %s", name)
	}
}
