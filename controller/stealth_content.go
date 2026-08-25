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

var stealthWords = []string{
	"river", "market", "signal", "harbor", "circuit", "meadow", "lantern", "quartz",
	"nomad", "cipher", "orbit", "thicket", "beacon", "cobalt", "drizzle", "ember",
	"fjord", "granite", "hollow", "ivory", "jasper", "kelp", "lagoon", "mosaic",
	"nectar", "opal", "prairie", "quiver", "ridge", "saffron", "tundra", "velvet",
}

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
	"sunlight through a cafe window on a rainy afternoon",
	"an empty bench under autumn leaves in a city park",
	"snow-covered rooftops seen from a train window",
	"a lighthouse on a cliff during a storm, long exposure",
	"wildflowers along a dirt road in early summer",
	"a cat sleeping on a windowsill, warm afternoon light",
	"night market stalls with glowing lanterns in the rain",
	"a desert highway at golden hour with distant mesas",
	"close-up of raindrops on a window with city bokeh behind",
	"an old library reading room with dust motes in the light",
	"fishing boats resting on a beach at low tide, overcast",
	"a mountain trail disappearing into morning mist",
}

func StealthImagePrompt() string { return stealthImagePrompts[stealthRand(len(stealthImagePrompts))] }

var stealthRerankSets = [][3]any{} // query, doc1, doc2 triplets below

func init() {
	stealthRerankSets = [][3]any{
		{"how do plants make energy", "Photosynthesis converts sunlight into chemical energy inside leaves.", "Bread dough rises when yeast releases gas."},
		{"best way to store fresh herbs", "Wrap herbs in a damp paper towel before refrigerating.", "Marathon training requires gradual mileage increases."},
		{"why does bread dough rise", "Yeast ferments sugars and releases carbon dioxide gas.", "Solar panels convert light directly into electricity."},
		{"how to descale a kettle", "Fill the kettle with equal parts water and vinegar, boil, then rinse thoroughly.", "Deciduous trees shed their leaves to conserve water during winter."},
		{"what causes the seasons on earth", "Earth's axial tilt changes how directly sunlight strikes each hemisphere through the year.", "Sourdough starter needs daily feeding to stay active."},
		{"tips for better sleep quality", "Keep a consistent bedtime and avoid screens for an hour before sleeping.", "The Mediterranean diet emphasizes olive oil, fish, and vegetables."},
		{"how do noise cancelling headphones work", "They generate inverse sound waves to cancel incoming ambient noise.", "Compost piles need a balance of green and brown materials."},
		{"why is the ocean salty", "Rivers dissolve minerals from rocks and carry them to the sea, where they concentrate over time.", "Bicycle gears transfer pedal power to the rear wheel through a chain."},
	}
}

func StealthRerank() (string, []any) {
	s := stealthRerankSets[stealthRand(len(stealthRerankSets))]
	return s[0].(string), []any{s[1], s[2]}
}

// StealthMaxTokens jitters small fixed token limits (base..base+47).
func StealthMaxTokens(base uint) uint { return base + uint(stealthRand(48)) }
