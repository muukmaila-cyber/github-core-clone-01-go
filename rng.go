package main

// Deterministic scenario RNG, re-implemented from the kit's wire.ts contract:
// FNV-1a 64-bit label hashing, splitmix64 finalizer, xorshift64* stream.
// The viewer identity is derived from this stream at startup, never hardcoded.

const (
	scenarioSeed uint64 = 0x6a09e667f3bcc909
	scenarioEpoch int64  = 1735689600 // 2025-01-01T00:00:00Z
	u64Mask      uint64 = 0xffffffffffffffff
)

func fnv1a64(text string) uint64 {
	hash := uint64(0xcbf29ce484222325)
	for i := 0; i < len(text); i++ {
		hash ^= uint64(text[i])
		hash *= 0x100000001b3
	}
	return hash
}

func mix64(value uint64) uint64 {
	z := value + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func scenarioSeedFor(label string) uint64 {
	return mix64(scenarioSeed ^ fnv1a64(label))
}

type rng struct {
	state uint64
}

func newRng(seed uint64) *rng {
	masked := seed & u64Mask
	if masked == 0 {
		masked = scenarioSeed
	}
	return &rng{state: masked}
}

func scenarioRng(label string) *rng {
	return newRng(scenarioSeedFor(label))
}

func (r *rng) nextU64() uint64 {
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 0x2545f4914f6cdd1d
}

func (r *rng) nextU32() uint32 {
	return uint32(r.nextU64() >> 32)
}

func (r *rng) nextInt(maxExclusive int) int {
	if maxExclusive == 1 {
		return 0
	}
	const range32 = 4294967296
	limit := range32 - (range32 % uint64(maxExclusive))
	for {
		v := uint64(r.nextU32())
		if v < limit {
			return int(v % uint64(maxExclusive))
		}
	}
}

func (r *rng) pick(items []string) string {
	return items[r.nextInt(len(items))]
}

var firstNames = []string{
	"ada", "bram", "cora", "dmitri", "elena", "felix", "greta", "hugo", "iris", "jonas", "kira", "lars",
	"mira", "nico", "olga", "pavel", "quinn", "rosa", "sven", "tess", "ulla", "vito", "wren", "yara", "zane",
}

var lastNames = []string{
	"anders", "bauer", "costa", "dahl", "eriksen", "fischer", "garcia", "holm", "iversen", "jansen", "keller",
	"lind", "moreau", "novak", "olsen", "peters", "quist", "rossi", "sato", "turi", "ulrich", "vega", "weber",
	"xu", "yilmaz", "zimmer",
}

// deriveViewer reproduces the suite's users[0]: first pick from FIRST_NAMES,
// then from LAST_NAMES, login = first+last lowercased, id 1000.
func deriveViewer() *User {
	r := scenarioRng("users")
	first := r.pick(firstNames)
	last := r.pick(lastNames)
	login := first + last
	return &User{
		ID:    1000,
		Login: login,
		Name:  first + " " + last,
		Email: login + "@example.invalid",
		Type:  "User",
	}
}
