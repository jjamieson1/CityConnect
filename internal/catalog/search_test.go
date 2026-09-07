package catalog

import (
	"strings"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// A catalogue shaped like a real one: official names a resident would not
// guess, and the words they use instead.
func testCatalogue() []domain.ServiceType {
	return []domain.ServiceType{
		{Code: "POTHOLE", Name: "Pothole repair", Category: "Roads & transport",
			Description: "Report a pothole or damaged road surface.",
			Synonyms:    "pot hole, road damage, hole in the road, broken road"},
		{Code: "STREETLIGHT", Name: "Streetlight outage", Category: "Roads & transport",
			Description: "A street light that is out, flickering or damaged.",
			Synonyms:    "street light, lamp post, dark street, light out"},
		{Code: "GRAFFITI", Name: "Graffiti removal", Category: "Parks & public space",
			Description: "Graffiti on City property.",
			Synonyms:    "tagging, vandalism, spray paint"},
		{Code: "GARBAGE", Name: "Missed garbage collection", Category: "Waste",
			Description: "Your bin was not emptied on collection day.",
			Synonyms:    "bin, rubbish, trash, refuse, missed pickup"},
		{Code: "PARKING", Name: "Parking permit", Category: "Parking",
			Description: "Apply for or renew a residential parking permit.",
			Synonyms:    "permit, resident parking"},
		{Code: "TREE", Name: "Tree maintenance", Category: "Parks & public space",
			Description: "Overhanging branches, fallen limbs or a damaged tree.",
			Synonyms:    "branch, fallen tree, overhanging"},
	}
}

func topResult(t *testing.T, query string) string {
	t.Helper()
	got := Search(testCatalogue(), query)
	if len(got) == 0 {
		t.Fatalf("search %q returned nothing", query)
	}
	return got[0].Code
}

// The demo beat, and the whole reason this exists: residents do not know the
// City's internal service names.
func TestSearchFindsServicesByTheWordsResidentsActuallyUse(t *testing.T) {
	cases := map[string]string{
		"pot hole":         "POTHOLE",
		"hole in the road": "POTHOLE",
		"road damage":      "POTHOLE",
		"street light":     "STREETLIGHT",
		"lamp post":        "STREETLIGHT",
		"tagging":          "GRAFFITI",
		"bin":              "GARBAGE",
		"rubbish":          "GARBAGE",
		"missed pickup":    "GARBAGE",
		"fallen tree":      "TREE",
		"resident parking": "PARKING",
	}
	for query, want := range cases {
		t.Run(query, func(t *testing.T) {
			if got := topResult(t, query); got != want {
				t.Errorf("search %q ranked %s first, want %s", query, got, want)
			}
		})
	}
}

// Somebody standing at the roadside on a phone will mistype.
func TestSearchToleratesTypos(t *testing.T) {
	cases := map[string]string{
		"pothole":     "POTHOLE",     // run together, catalogue has two words
		"potohle":     "POTHOLE",     // transposed
		"pothle":      "POTHOLE",     // dropped letter
		"grafitti":    "GRAFFITI",    // the common misspelling
		"streetlight": "STREETLIGHT", // run together
		"garbge":      "GARBAGE",
	}
	for query, want := range cases {
		t.Run(query, func(t *testing.T) {
			if got := topResult(t, query); got != want {
				t.Errorf("search %q ranked %s first, want %s", query, got, want)
			}
		})
	}
}

// A correctly spelled match must never lose to a fuzzy one, or searching for a
// real service returns somebody else's typo.
func TestExactMatchesOutrankFuzzyOnes(t *testing.T) {
	got := Search(testCatalogue(), "tree")
	if len(got) == 0 {
		t.Fatal("no results")
	}
	if got[0].Code != "TREE" {
		t.Errorf("ranked %s above the exact match TREE", got[0].Code)
	}
}

// Every term has to land somewhere. Without that, a two-word query returns
// everything matching either word — a longer list and a worse answer.
func TestAllTermsMustMatch(t *testing.T) {
	if got := Search(testCatalogue(), "parking permit"); len(got) == 0 || got[0].Code != "PARKING" {
		t.Errorf("'parking permit' did not rank PARKING first: %v", codes(got))
	}
	// "pothole" and "graffiti" share no service, so demanding both finds none.
	if got := Search(testCatalogue(), "pothole graffiti"); len(got) != 0 {
		t.Errorf("a query whose terms share no service returned %v", codes(got))
	}
}

// The no-results case is a real answer that the UI turns into "try a different
// word, or browse" — it must be an empty list, not an error and not everything.
func TestNoMatchReturnsNothingRatherThanEverything(t *testing.T) {
	got := Search(testCatalogue(), "zxqwv")
	if len(got) != 0 {
		t.Errorf("nonsense query returned %v", codes(got))
	}
}

// An empty box is browsing, not searching.
func TestEmptyQueryReturnsTheWholeCatalogue(t *testing.T) {
	all := testCatalogue()
	for _, query := range []string{"", "   ", "!!!"} {
		if got := Search(all, query); len(got) != len(all) {
			t.Errorf("query %q returned %d of %d entries", query, len(got), len(all))
		}
	}
}

// A whole-word hit beats a fragment: "park" is Parks before Parking Permits
// only if the ranking says so.
func TestWholeWordBeatsFragment(t *testing.T) {
	got := Search([]domain.ServiceType{
		{Code: "PARKING", Name: "Parking permit", Synonyms: "permit"},
		{Code: "PARK", Name: "Park maintenance", Synonyms: "park, green space"},
	}, "park")
	if len(got) < 2 {
		t.Fatalf("expected both, got %v", codes(got))
	}
	if got[0].Code != "PARK" {
		t.Errorf("ranked %v; the whole-word match should come first", codes(got))
	}
}

// Ranking has to be stable, or the same search reorders itself between
// keystrokes and the option under the cursor moves.
func TestRankingIsStable(t *testing.T) {
	first := codes(Search(testCatalogue(), "road"))
	for i := 0; i < 20; i++ {
		if got := codes(Search(testCatalogue(), "road")); got != first {
			t.Fatalf("run %d returned %s, first run returned %s", i, got, first)
		}
	}
}

// Case and punctuation are the resident's business, not ours.
func TestSearchIgnoresCaseAndPunctuation(t *testing.T) {
	for _, query := range []string{"Pot-Hole", "POT HOLE", "pot   hole.", "pot,hole"} {
		if got := topResult(t, query); got != "POTHOLE" {
			t.Errorf("search %q ranked %s first, want POTHOLE", query, got)
		}
	}
}

// Three letters and an edit distance of one matches half the dictionary, so
// short words are matched exactly or not at all.
func TestShortTermsAreNotFuzzyMatched(t *testing.T) {
	// "bit" is one edit from "bin" but is not what anyone meant.
	if got := Search(testCatalogue(), "bit"); len(got) != 0 {
		t.Errorf("a three-letter typo matched %v", codes(got))
	}
}

func codes(entries []domain.ServiceType) string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Code)
	}
	return strings.Join(out, ",")
}
