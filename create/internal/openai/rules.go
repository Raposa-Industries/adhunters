package openai

import (
	"bufio"
	"embed"
	"math/rand/v2"
	"regexp"
	"strings"
)

// The team's own material, kept as they wrote it (rules/): the headlines that
// ran for each vertical, and the words they have seen Taboola block.
//
//go:embed rules/blocked.txt rules/headlines/*.txt
var rulesFS embed.FS

// Blocked is one word or phrase Taboola has blocked for the team. Description
// is true when it was blocked in descriptions as well as titles.
type Blocked struct {
	Text        string `json:"text"`
	Description bool   `json:"description"`
}

// BlockedWords is the team's list, in the file's order.
var BlockedWords = mustBlocked()

func mustBlocked() []Blocked {
	raw, err := rulesFS.ReadFile("rules/blocked.txt")
	if err != nil {
		panic(err)
	}
	return parseBlocked(string(raw))
}

func parseBlocked(s string) []Blocked {
	var out []Blocked
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		where, text, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, Blocked{Text: text, Description: strings.TrimSpace(where) == "title+description"})
	}
	return out
}

// headlineFiles maps the page's verticals to the team's headline files.
var headlineFiles = map[string]string{
	"memory loss":    "rules/headlines/memory-loss.txt",
	"weight loss":    "rules/headlines/weight-loss.txt",
	"tinnitus":       "rules/headlines/tinnitus.txt",
	"neuropathy":     "rules/headlines/neuropathy.txt",
	"blood pressure": "rules/headlines/blood-pressure.txt",
}

// ExampleVerticals lists the verticals that have team headlines.
func ExampleVerticals() []string {
	return []string{"Blood Pressure", "Memory Loss", "Neuropathy", "Tinnitus", "Weight Loss"}
}

// Library is one vertical's team examples.
type Library struct {
	Headlines    []string
	Descriptions []string
}

var libraries = mustLibraries()

func mustLibraries() map[string]Library {
	out := map[string]Library{}
	for v, path := range headlineFiles {
		raw, err := rulesFS.ReadFile(path)
		if err != nil {
			panic(err)
		}
		out[v] = parseLibrary(string(raw))
	}
	return out
}

// LibraryFor returns the team's examples for a vertical, if there are any.
func LibraryFor(vertical string) (Library, bool) {
	l, ok := libraries[strings.ToLower(strings.TrimSpace(vertical))]
	return l, ok
}

var (
	rule        = regexp.MustCompile(`^[_\-=—\s]+$`)
	descHeading = regexp.MustCompile(`(?i)^(descriptions?|descrições|descricoes)\b`)
	headHeading = regexp.MustCompile(`(?i)^(head originais|headlines?\s*\()`)
)

// parseLibrary reads a team file: headlines until a "Descriptions" heading,
// descriptions after it. Separators, section headings and exact repeats are
// dropped, and the Unicode "bold" letters some headlines are typed in become
// plain letters, since Taboola rejects them.
func parseLibrary(s string) Library {
	var lib Library
	seen := map[string]bool{}
	desc := false
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := CleanLine(Plain(strings.TrimPrefix(sc.Text(), "\uFEFF")))
		switch {
		case line == "" || rule.MatchString(line):
			continue
		case descHeading.MatchString(line) && len([]rune(line)) < 40:
			desc = true
			continue
		case headHeading.MatchString(line):
			continue
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		if desc {
			lib.Descriptions = append(lib.Descriptions, line)
		} else {
			lib.Headlines = append(lib.Headlines, line)
		}
	}
	return lib
}

// Plain turns the Mathematical Alphanumeric letters and digits (bold, italic
// and the rest, U+1D400 to U+1D7FF) into plain ASCII.
func Plain(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0x1D400 && r <= 0x1D6A3:
			// 13 alphabets of 52 letters each (A-Z then a-z).
			i := (r - 0x1D400) % 52
			if i < 26 {
				return 'A' + i
			}
			return 'a' + i - 26
		case r >= 0x1D7CE && r <= 0x1D7FF:
			return '0' + (r-0x1D7CE)%10
		}
		return r
	}, s)
}

// sample picks up to n lines at random, keeping the file's order, so every
// plan sees a different slice of a long library.
func sample(lines []string, n int, rnd *rand.Rand) []string {
	if len(lines) <= n {
		return lines
	}
	idx := rnd.Perm(len(lines))[:n]
	keep := make([]bool, len(lines))
	for _, i := range idx {
		keep[i] = true
	}
	out := make([]string, 0, n)
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
		}
	}
	return out
}
