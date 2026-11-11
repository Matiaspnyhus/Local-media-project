package ids

import "testing"

func TestNewIsValidAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := New()
		if !Valid(id) {
			t.Fatalf("invalid id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestGUID(t *testing.T) {
	got := GUID("0123456789abcdef0123456789abcdef")
	if got != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("got %q", got)
	}
	if Valid("../etc/passwd") || Valid("0123456789ABCDEF0123456789abcdef") {
		t.Fatal("accepted a bad id")
	}
}
