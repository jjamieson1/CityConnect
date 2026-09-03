package requests

import (
	"regexp"
	"strings"
	"testing"
)

// referenceShape is the contract a citizen sees: a prefix and two groups of
// four, drawn from the Crockford alphabet.
var referenceShape = regexp.MustCompile(`^[A-Z0-9]{1,8}(-[0-9A-HJKMNP-TV-Z]{4}){2}$`)

func TestNewReferenceShape(t *testing.T) {
	ref, err := NewReference("BBY")
	if err != nil {
		t.Fatalf("NewReference: %v", err)
	}
	if !referenceShape.MatchString(ref) {
		t.Errorf("reference %q does not match the documented shape", ref)
	}
	if !strings.HasPrefix(ref, "BBY-") {
		t.Errorf("reference %q does not carry the configured prefix", ref)
	}
}

// The whole point of the change: a reference must not be derivable from the
// one before it. Sequential values would collide here immediately.
func TestNewReferenceIsNotSequential(t *testing.T) {
	const draws = 500

	seen := make(map[string]bool, draws)
	for i := 0; i < draws; i++ {
		ref, err := NewReference("SR")
		if err != nil {
			t.Fatalf("NewReference: %v", err)
		}
		if seen[ref] {
			t.Fatalf("reference %q drawn twice in %d draws", ref, draws)
		}
		seen[ref] = true
	}
}

// The alphabet exists to survive a phone call. A reference that can contain
// the glyphs people confuse defeats the normalisation that reads them back.
func TestNewReferenceOmitsAmbiguousLetters(t *testing.T) {
	for i := 0; i < 500; i++ {
		ref, err := NewReference("SR")
		if err != nil {
			t.Fatalf("NewReference: %v", err)
		}
		if body := strings.TrimPrefix(ref, "SR"); strings.ContainsAny(body, "ILOU") {
			t.Fatalf("reference %q contains an ambiguous letter", ref)
		}
	}
}

func TestIsGeneratedReference(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"BBY-7K4M-2QX9", true},
		{"SR-A9FA-KSYV", true},
		{"SR-DEM0-0001", true}, // the fixed demo-seed value keeps the real shape
		{"SR-2026-000001", false},
		{"SR-DEMO-000001", false}, // O is not in the alphabet, and the tail is six long
		{"SR-7K4M", false},
		{"7K4M-2QX9", false},
		{"sr-7k4m-2qx9", false}, // stored references are canonical upper case
		{"", false},
	}
	for _, c := range cases {
		if got := IsGeneratedReference(c.in); got != c.want {
			t.Errorf("IsGeneratedReference(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Whatever the generator emits must be recognised by the checker, or the
// reissue command would keep rewriting rows it had already converted.
func TestGeneratedReferencesAreRecognised(t *testing.T) {
	for _, prefix := range []string{"SR", "BBY", "A", "VERYLONG"} {
		for i := 0; i < 100; i++ {
			ref, err := NewReference(prefix)
			if err != nil {
				t.Fatalf("NewReference: %v", err)
			}
			if !IsGeneratedReference(ref) {
				t.Fatalf("generated %q is not recognised as generated", ref)
			}
		}
	}
}

func TestNormalizeReferencePrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"BBY", "BBY"},
		{"bby", "BBY"},
		{"  bby  ", "BBY"},
		{"B-B_Y!", "BBY"},
		{"", DefaultReferencePrefix},
		{"---", DefaultReferencePrefix},
		{"VERYLONGPREFIXINDEED", "VERYLONG"},
	}
	for _, c := range cases {
		if got := NormalizeReferencePrefix(c.in); got != c.want {
			t.Errorf("NormalizeReferencePrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A hyphen in a configured prefix would break the split NormalizeReference
// relies on to find the random half, so it must not survive normalisation.
func TestReferencePrefixCannotIntroduceAHyphen(t *testing.T) {
	ref, err := NewReference("A-B")
	if err != nil {
		t.Fatalf("NewReference: %v", err)
	}
	if got, want := strings.Count(ref, "-"), referenceGroups; got != want {
		t.Errorf("reference %q has %d hyphens, want %d", ref, got, want)
	}
}

func TestNormalizeReference(t *testing.T) {
	cases := []struct {
		name     string
		in, want string
	}{
		{"upper-cases and trims", "  bby-7k4m-2qx9 ", "BBY-7K4M-2QX9"},
		{"already canonical", "BBY-7K4M-2QX9", "BBY-7K4M-2QX9"},
		{"folds O to zero", "BBY-7K4M-2QXO", "BBY-7K4M-2QX0"},
		{"folds I and L to one", "BBY-7K4I-2QXL", "BBY-7K41-2QX1"},
		{"leaves the prefix alone", "IOU-7K4M-2QX9", "IOU-7K4M-2QX9"},
		{"historical format survives", "sr-2026-000001", "SR-2026-000001"},
		{"historical format tolerates O for zero", "SR-2O26-OOOOO1", "SR-2026-000001"},
		{"no hyphen, no folding", "BBY7K4M", "BBY7K4M"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeReference(c.in); got != c.want {
				t.Errorf("NormalizeReference(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Folding is only safe because no reference we can generate contains the
// letters being folded. If the alphabet ever gains one, this fails.
func TestFoldedLettersAreNotInTheAlphabet(t *testing.T) {
	if strings.ContainsAny(referenceAlphabet, "ILOU") {
		t.Fatal("referenceAlphabet contains a letter NormalizeReference folds; " +
			"lookup could now resolve one request's reference to another's")
	}
}
