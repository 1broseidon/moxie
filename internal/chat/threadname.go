package chat

import (
	"fmt"
	"math/rand"
)

// Word lists for generating human-friendly thread names.
// ~50 adjectives × ~50 nouns = ~2500 two-word combinations.
var adjectives = []string{
	"bold", "calm", "cool", "dark", "deep",
	"dry", "fair", "fast", "firm", "flat",
	"free", "full", "gold", "gray", "keen",
	"kind", "late", "lean", "long", "loud",
	"mild", "neat", "next", "odd", "pale",
	"pine", "pure", "raw", "red", "rich",
	"safe", "salt", "slim", "slow", "soft",
	"sure", "tall", "thin", "tiny", "true",
	"vast", "warm", "weak", "west", "wide",
	"wild", "wise", "worn", "blue", "mint",
}

var nouns = []string{
	"arch", "bay", "bird", "bolt", "cave",
	"claw", "dawn", "dune", "dust", "elm",
	"fern", "fire", "fish", "floe", "fox",
	"gate", "glen", "hare", "hawk", "hill",
	"jade", "knot", "lake", "leaf", "lynx",
	"mesa", "moth", "nest", "oak", "orb",
	"peak", "pine", "pond", "rain", "reef",
	"rim", "root", "sage", "sand", "seal",
	"snow", "star", "stem", "swan", "tide",
	"vale", "vine", "wave", "wolf", "wren",
}

var extras = []string{
	"ash", "bay", "cove", "dale", "edge",
	"ford", "gale", "haze", "isle", "jet",
	"keel", "lark", "mist", "node", "opal",
	"pyre", "quay", "rift", "spur", "tarn",
}

// NewThreadName generates a unique human-friendly thread name.
// It checks existing against the provided set and appends a third word
// if two-word combinations are exhausted.
func NewThreadName(existing map[string]bool) string {
	// Try two-word combos in random order
	order := rand.Perm(len(adjectives) * len(nouns))
	for _, idx := range order {
		a := adjectives[idx/len(nouns)]
		n := nouns[idx%len(nouns)]
		name := a + "-" + n
		if !existing[name] {
			return name
		}
	}

	// All two-word names taken — append a third word
	for _, idx := range order {
		a := adjectives[idx/len(nouns)]
		n := nouns[idx%len(nouns)]
		for _, e := range extras {
			name := a + "-" + n + "-" + e
			if !existing[name] {
				return name
			}
		}
	}

	// Absolute fallback (should never happen: 2500 × 20 = 50,000 combos)
	return fmt.Sprintf("chat-%d", rand.Int63())
}
