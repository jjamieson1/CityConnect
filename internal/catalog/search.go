package catalog

import (
	"sort"
	"strings"
	"unicode"

	"github.com/jjamieson1/CityConnect/internal/domain"
)

// Search ranks a catalogue against what a resident typed.
//
// Deliberately done in Go over an already-loaded slice rather than in SQL. A
// municipal service catalogue is tens of entries, occasionally a few hundred —
// scanning it costs microseconds, and doing it here buys three things that
// matter more than the theoretical scaling:
//
//   - Typo tolerance without database-specific features. MySQL full-text does
//     not do edit distance, and the workarounds are worse than this.
//   - The same behaviour in tests as in production. Search that only works on
//     MySQL is search nobody can test.
//   - No operational dependency. A search engine is a per-engagement cost and a
//     question in the security review, for a problem this size.
//
// If a catalogue ever reaches thousands of entries this needs revisiting — but
// a city with thousands of distinct citizen-facing services has a bigger
// problem than its search implementation.
func Search(entries []domain.ServiceType, query string) []domain.ServiceType {
	terms := tokenize(query)
	if len(terms) == 0 {
		return entries
	}

	type scored struct {
		entry domain.ServiceType
		score int
	}

	ranked := make([]scored, 0, len(entries))
	for _, e := range entries {
		if s := scoreEntry(e, terms); s > 0 {
			ranked = append(ranked, scored{entry: e, score: s})
		}
	}

	// Score first, then name, so a repeated search returns the same order
	// rather than whatever the map iteration or the database felt like.
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].entry.Name < ranked[j].entry.Name
	})

	out := make([]domain.ServiceType, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.entry)
	}
	return out
}

// Field weights. The ordering is the whole of the relevance model: a hit in the
// service's name means more than one in its description, and a synonym is
// weighted like a name because a synonym *is* the name as far as the resident
// is concerned — they are not using the wrong word, we are.
const (
	weightName        = 10
	weightSynonym     = 9
	weightCategory    = 4
	weightDescription = 2

	// A whole-word hit beats a fragment: "park" should find Parks before it
	// finds Parking Permits.
	bonusWholeWord = 6
	bonusPrefix    = 3
	// A fuzzy hit counts, but never outranks something spelled correctly.
	penaltyFuzzy = -4
)

func scoreEntry(e domain.ServiceType, terms []string) int {
	fields := []struct {
		text   string
		weight int
	}{
		{e.Name, weightName},
		{e.Synonyms, weightSynonym},
		{e.Category, weightCategory},
		{e.Description, weightDescription},
	}

	total := 0
	for _, term := range terms {
		best := 0
		for _, f := range fields {
			if s := scoreField(f.text, term, f.weight); s > best {
				best = s
			}
		}
		// Every term has to land somewhere. Without this, searching "dog
		// licence" would return every service mentioning dogs *and* every
		// service mentioning licences, which is a longer list and a worse
		// answer than none.
		if best == 0 {
			return 0
		}
		total += best
	}
	return total
}

func scoreField(text, term string, weight int) int {
	words := tokenize(text)
	if len(words) == 0 {
		return 0
	}

	best := 0
	for _, w := range words {
		switch {
		case w == term:
			best = max(best, weight+bonusWholeWord)
		case strings.HasPrefix(w, term):
			best = max(best, weight+bonusPrefix)
		case strings.Contains(w, term):
			best = max(best, weight)
		case closeEnough(w, term):
			// "potohle" for "pothole", or a doubled letter. Scored below an
			// exact hit so a correctly spelled match always wins.
			best = max(best, weight+penaltyFuzzy)
		}
	}

	// A run-together query — "pothole" typed for the two-word "pot hole", or
	// the reverse — matches nothing word by word, so compare against the field
	// with its spaces removed.
	if best == 0 {
		if joined := strings.Join(words, ""); strings.Contains(joined, term) {
			best = weight
		}
	}
	return best
}

// closeEnough reports whether two words are within a typo of each other.
//
// The tolerance scales with length: nobody needs help with "bin", and one
// mistyped character in "streetlight" should not lose the result. Short words
// are excluded entirely, because at three letters an edit distance of one
// matches half the dictionary.
func closeEnough(word, term string) bool {
	if len(term) < 4 {
		return false
	}
	allowed := 1
	if len(term) >= 8 {
		allowed = 2
	}
	// A length gap wider than the tolerance cannot be closed, and checking
	// first avoids the matrix entirely for most pairs.
	if abs(len(word)-len(term)) > allowed {
		return false
	}
	return editDistance(word, term) <= allowed
}

// editDistance is optimal string alignment — Levenshtein plus transposition.
//
// The transposition case is the reason this is not plain Levenshtein. Swapping
// two adjacent letters is the single most common typo, especially on a phone
// keyboard: "potohle" for "pothole", "teh" for "the". Levenshtein scores that
// as two edits, which puts it outside a one-edit tolerance and loses the
// result; counting it as the one slip it actually is finds it.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)

	// Three rows: the transposition check needs the row before last.
	prev2 := make([]int, len(br)+1)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)

			if i > 1 && j > 1 && ar[i-1] == br[j-2] && ar[i-2] == br[j-1] {
				curr[j] = min(curr[j], prev2[j-2]+1)
			}
		}
		prev2, prev, curr = prev, curr, prev2
	}
	return prev[len(br)]
}

// tokenize lowercases and splits on anything that is not a letter or digit, so
// "pot-hole", "pot hole" and "Pot Hole." all reduce to the same two words.
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
