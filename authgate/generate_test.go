package authgate

import (
	"strings"
	"testing"
)

func TestGenerate_ShapeAndMatch(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c := Generate()
		// Shape: 8 alphabet chars grouped 4-4 with a hyphen → "XXXX-XXXX".
		if len(c) != 9 || c[4] != '-' {
			t.Fatalf("Generate() = %q, want XXXX-XXXX", c)
		}
		body := strings.ReplaceAll(c, "-", "")
		for _, r := range body {
			if !strings.ContainsRune(authCodeAlphabet, r) {
				t.Fatalf("Generate() = %q has rune %q outside the alphabet", c, r)
			}
		}
		// A freshly generated code must authenticate against itself (round-trips through NormalizeCode).
		if !CodeMatches(c, c) {
			t.Fatalf("Generate() = %q does not match itself", c)
		}
		if seen[c] {
			t.Fatalf("Generate() produced a duplicate within 200 draws: %q", c)
		}
		seen[c] = true
	}
}
