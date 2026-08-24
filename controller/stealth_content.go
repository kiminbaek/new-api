package controller

// [CUSTOM] stealth: randomized channel-test content so upstream providers
// cannot fingerprint new-api deployments by their fixed test prompts
// ("hi" / "hello world" / "a cute cat" / fixed Deep Learning rerank set).

import (
	"encoding/json"
	"fmt"
	"math/rand"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

var stealthWords = []string{"river", "market", "signal", "harbor", "circuit", "meadow", "lantern", "quartz", "nomad", "cipher", "orbit", "thicket"}

func stealthRand(n int) int { return rand.Intn(n) }

// StealthMathPrompt returns a random arithmetic question that looks like
// ordinary human traffic instead of a gateway health-check ping.
func StealthMathPrompt() string {
	ops := []string{"+", "-", "*"}
	op := ops[stealthRand(len(ops))]
	a, b := stealthRand(900)+100, stealthRand(90)+10
	switch op {
	case "-":
		if b > a {
			a, b = b, a
		}
	case "*":
		a, b = stealthRand(90)+10, stealthRand(9)+2
	}
	return fmt.Sprintf("What is %d %s %d? Reply with the number only.", a, op, b)
}

// StealthMessagesRaw builds chat messages carrying the random math prompt.
func StealthMessagesRaw() json.RawMessage {
	b, _ := json.Marshal([]dto.Message{{Role: "user", Content: StealthMathPrompt()}})
	return b
}

func StealthEmbeddingInput() string {
	w := func() string { return stealthWords[stealthRand(len(stealthWords))] }
	return fmt.Sprintf("%s %s near %s district %d", w(), w(), w(), stealthRand(1000))
}

var stealthImagePrompts = []string{
	"a red kite over quiet hills at dusk",
	"morning fog between tall pine trees",
	"a small wooden boat on a calm lake, wide shot",
	"old stone bridge after light rain",
}

func StealthImagePrompt() string { return stealthImagePrompts[stealthRand(len(stealthImagePrompts))] }

var stealthRerankSets = [][3]any{} // query, doc1, doc2 triplets below

func init() {
	stealthRerankSets = [][3]any{
		{"how do plants make energy", "Photosynthesis converts sunlight into chemical energy inside leaves.", "Bread dough rises when yeast releases gas."},
		{"best way to store fresh herbs", "Wrap herbs in a damp paper towel before refrigerating.", "Marathon training requires gradual mileage increases."},
		{"why does bread dough rise", "Yeast ferments sugars and releases carbon dioxide gas.", "Solar panels convert light directly into electricity."},
	}
}

func StealthRerank() (string, []any) {
	s := stealthRerankSets[stealthRand(len(stealthRerankSets))]
	return s[0].(string), []any{s[1], s[2]}
}

// StealthMaxTokens jitters small fixed token limits (base..base+47).
func StealthMaxTokens(base uint) uint { return base + uint(stealthRand(48)) }
