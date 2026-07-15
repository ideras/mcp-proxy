package main

import (
	"errors"
	"testing"
)

func newTestClient(name string, mode ConflictMode, namespace string, registry *nameRegistry) *Client {
	return &Client{
		name:         name,
		registry:     registry,
		namespace:    namespace,
		registerMode: mode,
	}
}

func TestNameRegistryClaim(t *testing.T) {
	r := newNameRegistry()
	if !r.claim("a") {
		t.Fatal("first claim of \"a\" should succeed")
	}
	if r.claim("a") {
		t.Fatal("second claim of \"a\" should fail (collision)")
	}
	if !r.claim("b") {
		t.Fatal("claim of \"b\" should succeed")
	}
}

func TestApplyNameAndURI(t *testing.T) {
	// standalone: no namespace applied
	standalone := &Client{namespace: "", registerMode: ConflictModePrefix}
	if got := standalone.applyName("search"); got != "search" {
		t.Fatalf("standalone applyName = %q, want %q", got, "search")
	}
	if got := standalone.applyURI("file:///a"); got != "file:///a" {
		t.Fatalf("standalone applyURI = %q, want %q", got, "file:///a")
	}

	// prefix mode: names dotted, URIs slash-prefixed
	prefixed := &Client{namespace: "github", registerMode: ConflictModePrefix}
	if got := prefixed.applyName("search"); got != "github-search" {
		t.Fatalf("prefix applyName = %q, want %q", got, "github-search")
	}
	if got := prefixed.applyURI("file:///a"); got != "github/file:///a" {
		t.Fatalf("prefix applyURI = %q, want %q", got, "github/file:///a")
	}

	// error / first-wins modes never namespace, even with a namespace set
	errored := &Client{namespace: "github", registerMode: ConflictModeError}
	if got := errored.applyName("search"); got != "search" {
		t.Fatalf("error applyName = %q, want %q (no namespacing)", got, "search")
	}
	if got := errored.applyURI("file:///a"); got != "file:///a" {
		t.Fatalf("error applyURI = %q, want %q (no namespacing)", got, "file:///a")
	}
}

func TestNamespaceActive(t *testing.T) {
	cases := []struct {
		mode ConflictMode
		ns   string
		want bool
	}{
		{ConflictModePrefix, "github", true},
		{ConflictModePrefix, "", false}, // no namespace -> not active
		{ConflictModeError, "github", false},
		{ConflictModeFirstWins, "github", false},
		{"", "", false},
	}
	for _, tc := range cases {
		cl := &Client{namespace: tc.ns, registerMode: tc.mode}
		if got := cl.namespaceActive(); got != tc.want {
			t.Fatalf("namespaceActive(mode=%s, ns=%q) = %v, want %v", tc.mode, tc.ns, got, tc.want)
		}
	}
}

func TestResourceConflictPrefixNoCollision(t *testing.T) {
	// Two members with the same tool name under prefix mode must both register:
	// their namespaced keys differ.
	reg := newNameRegistry()
	a := newTestClient("alpha", ConflictModePrefix, "alpha", reg)
	b := newTestClient("beta", ConflictModePrefix, "beta", reg)

	if ok, err := a.resourceConflict("tool", "search"); !ok || err != nil {
		t.Fatalf("alpha first tool: ok=%v err=%v", ok, err)
	}
	if ok, err := b.resourceConflict("tool", "search"); !ok || err != nil {
		t.Fatalf("beta same-named tool under prefix: ok=%v err=%v (should both register)", ok, err)
	}
}

func TestResourceConflictErrorModeFatal(t *testing.T) {
	reg := newNameRegistry()
	a := newTestClient("alpha", ConflictModeError, "alpha", reg)
	b := newTestClient("beta", ConflictModeError, "beta", reg)

	if ok, err := a.resourceConflict("tool", "search"); !ok || err != nil {
		t.Fatalf("alpha first tool: ok=%v err=%v", ok, err)
	}
	ok, err := b.resourceConflict("tool", "search")
	if ok {
		t.Fatal("beta duplicate tool under error mode should NOT be allowed")
	}
	var ce *collisionError
	if !errors.As(err, &ce) {
		t.Fatalf("beta duplicate should return *collisionError, got %T: %v", err, err)
	}
	if ce.kind != "tool" || ce.name != "search" {
		t.Fatalf("collisionError fields wrong: kind=%q name=%q", ce.kind, ce.name)
	}
}

func TestResourceConflictFirstWinsSkips(t *testing.T) {
	reg := newNameRegistry()
	a := newTestClient("alpha", ConflictModeFirstWins, "alpha", reg)
	b := newTestClient("beta", ConflictModeFirstWins, "beta", reg)

	if ok, err := a.resourceConflict("prompt", "review"); !ok || err != nil {
		t.Fatalf("alpha first prompt: ok=%v err=%v", ok, err)
	}
	if ok, err := b.resourceConflict("prompt", "review"); ok || err != nil {
		t.Fatalf("beta duplicate prompt under first-wins: ok=%v err=%v (should skip silently)", ok, err)
	}
}

func TestResourceConflictStandaloneNeverTracks(t *testing.T) {
	// registry == nil means standalone mode: always allow, never track.
	standalone := &Client{} // registry nil
	for i := 0; i < 3; i++ {
		if ok, err := standalone.resourceConflict("tool", "dup"); !ok || err != nil {
			t.Fatalf("standalone iteration %d should always allow: ok=%v err=%v", i, ok, err)
		}
	}
}
