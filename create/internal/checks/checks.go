// Package checks warns about what in a headline is likely to break Taboola's
// title rules, the same rules the launcher page checks (create/web/launcher/
// checks.js, read from the Realize help center on 2026-09-29) and the team's
// list of words Taboola has blocked for them. They only warn: the person
// always has the final say, so nothing here blocks a save.
package checks

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Raposa-Industries/adhunters/create/internal/openai"
)

// Warning is one thing to look at in a headline, in pt-BR. Blocked and
// Alternatives are set for a word on the team's blocked list, so the page
// can offer a swap.
type Warning struct {
	Kind         string   `json:"kind"`
	Message      string   `json:"message"`
	Blocked      string   `json:"blocked,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
}

// Kinds of warning.
const (
	KindHidden  = "hidden"
	KindLength  = "length"
	KindCaps    = "caps"
	KindRule    = "rule"
	KindBlocked = "blocked"
)

// Short words that are fine in capitals (Taboola itself asks for "ED").
var acronyms = map[string]bool{"USA": true, "FDA": true, "DNA": true, "CBD": true, "NYC": true, "HVAC": true,
	"LED": true, "BMI": true, "HDL": true, "LDL": true, "ADHD": true, "COVID": true, "AARP": true}

var rules = []struct {
	re  *regexp.Regexp
	say func(m string) string
}{
	{regexp.MustCompile(`!!`), func(string) string { return "Mais de um ponto de exclamação: o Taboola recusa." }},
	{regexp.MustCompile(`(?i)\b(wow|shocking|never)\b`), func(m string) string { return "“" + m + "” é sensacionalista para o Taboola." }},
	{regexp.MustCompile(`(?i)\b(cures?|cured|prevents?|stops?|reverses?|reversed|get rid of|end years of|disappears?|eliminates?)\b`),
		func(m string) string { return "Promete resultado absoluto (“" + m + "”): o Taboola rejeita." }},
	{regexp.MustCompile(`(?i)diabet`), func(string) string { return "Cita diabetes: anúncios dessa vertical não podem usar a palavra." }},
	{regexp.MustCompile(`(?i)\b(cancer|câncer|alzheimer'?s?|dementia|neuropathy|arthritis|hypertension|glaucoma|cataracts?)\b`),
		func(m string) string {
			return "Cita uma doença (“" + m + "”): o Taboola quer sintomas, não doenças."
		}},
	{regexp.MustCompile(`(?i)erectile dysfunction|viagra`), func(string) string { return "Use “ED”, nunca “erectile dysfunction” ou “Viagra”." }},
	{regexp.MustCompile(`(?i)(\$\s?\d|R\$\s?\d|\d+\s?(lbs?|pounds|kg|kilos|quilos|dollars|dólares|reais)\b)`),
		func(string) string { return "Traz um valor (peso ou dinheiro): o Taboola não aceita." }},
}

var word = regexp.MustCompile(`\p{L}+`)

// Headline lists the warnings for one headline, the team's blocked words
// last.
func Headline(text string) []Warning {
	out := []Warning{}
	if Hidden(text) {
		out = append(out, Warning{Kind: KindHidden, Message: "Tem caracteres invisíveis: o Taboola trata como burla da revisão."})
	}
	t := openai.CleanLine(text)
	if n := utf8.RuneCountInString(t); n > 60 {
		out = append(out, Warning{Kind: KindLength, Message: fmt.Sprintf("%d caracteres: passa de 60 (o Taboola recomenda 34 a 45).", n)})
	}
	for _, w := range word.FindAllString(t, -1) {
		if utf8.RuneCountInString(w) >= 3 && w == strings.ToUpper(w) && w != strings.ToLower(w) && !acronyms[w] {
			out = append(out, Warning{Kind: KindCaps, Message: "Palavra toda em maiúsculas (" + w + "): o Taboola recusa."})
			break
		}
	}
	for _, r := range rules {
		if m := r.re.FindString(t); m != "" {
			out = append(out, Warning{Kind: KindRule, Message: r.say(m)})
		}
	}
	if hasEmoji(t) {
		out = append(out, Warning{Kind: KindRule, Message: "Tem emoji."})
	}
	for _, b := range BlockedHits(t, openai.BlockedWords) {
		out = append(out, Warning{Kind: KindBlocked, Blocked: b.Text, Alternatives: b.Alternatives,
			Message: "Tem “" + b.Text + "”: está na lista de palavras que o Taboola já bloqueou para o time."})
	}
	return out
}

// Hidden reports whether text carries a character CleanLine removes.
func Hidden(text string) bool {
	for _, r := range text {
		if r == 0x200B || r == 0x200C || r == 0x200D || r == 0x2060 || r == 0xFEFF || r == 0x00AD || r == 0x180E ||
			(r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
			return true
		}
	}
	return false
}

func hasEmoji(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.So, r) && r >= 0x2190 || r >= 0x1F000 && r <= 0x1FAFF {
			return true
		}
	}
	return false
}

// BlockedHits lists the team's blocked words found in a headline. A single
// word also matches its common endings (drinks, drinking); "…" inside a
// phrase stands for anything. When a phrase is found, the words inside it
// are not named again ("memory loss", not also "memory").
func BlockedHits(text string, list []openai.Blocked) []openai.Blocked {
	var found []openai.Blocked
	for _, b := range list {
		if len(find(text, b.Text)) > 0 {
			found = append(found, b)
		}
	}
	var out []openai.Blocked
	for _, b := range found {
		inner := false
		for _, g := range found {
			if g.Text != b.Text && strings.Contains(strings.ToLower(g.Text), strings.ToLower(b.Text)) {
				inner = true
				break
			}
		}
		if !inner {
			out = append(out, b)
		}
	}
	return out
}

var (
	patternsMu sync.Mutex
	patterns   = map[string]*regexp.Regexp{}
)

func pattern(blocked string) *regexp.Regexp {
	patternsMu.Lock()
	defer patternsMu.Unlock()
	if re, ok := patterns[blocked]; ok {
		return re
	}
	var parts []string
	for _, p := range regexp.MustCompile(`…|\.\.\.`).Split(blocked, -1) {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, strings.Join(strings.Fields(regexp.QuoteMeta(p)), `\s+`))
		}
	}
	ending := "(?:s|es|ing|ed|ting|ful)?"
	if strings.ContainsFunc(strings.TrimSpace(blocked), unicode.IsSpace) {
		ending = ""
	}
	re := regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(` + strings.Join(parts, ".*") + ending + `)`)
	patterns[blocked] = re
	return re
}

// find returns where blocked occurs in text as a whole word: [start, end)
// of each match of the word itself.
func find(text, blocked string) [][2]int {
	var out [][2]int
	re := pattern(blocked)
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[2], m[3]
		// Try the longest ending first, then shorter ones, until the word ends
		// at a boundary (RE2 has no look-ahead).
		for end > start {
			if r, _ := utf8.DecodeRuneInString(text[end:]); end == len(text) || !(unicode.IsLetter(r) || unicode.IsNumber(r)) {
				break
			}
			end = trimEnding(text, start, end, blocked)
		}
		if end > start {
			out = append(out, [2]int{start, end})
		}
	}
	return out
}

// trimEnding gives up the ending a match took, or the whole match when it
// has none, so find can try the word without it.
func trimEnding(text string, start, end int, blocked string) int {
	base := len(strings.TrimSpace(blocked))
	if end-start > base {
		for _, e := range []string{"ting", "ing", "ful", "es", "ed", "s"} {
			if strings.HasSuffix(strings.ToLower(text[start:end]), e) && end-len(e)-start >= base {
				return end - len(e)
			}
		}
	}
	return start
}

// Swap puts alt in place of every whole-word occurrence of blocked in text,
// in the case of what it replaces: "Memory" becomes "Focus", "MEMORY"
// becomes "FOCUS", "memory" stays lower case.
func Swap(text, blocked, alt string) string {
	hits := find(text, blocked)
	for i := len(hits) - 1; i >= 0; i-- {
		h := hits[i]
		text = text[:h[0]] + inCase(alt, text[h[0]:h[1]]) + text[h[1]:]
	}
	return text
}

func inCase(alt, like string) string {
	letters := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		return -1
	}, like)
	if utf8.RuneCountInString(letters) > 1 && letters == strings.ToUpper(letters) {
		return strings.ToUpper(alt)
	}
	if r, _ := utf8.DecodeRuneInString(like); unicode.IsUpper(r) {
		words := strings.Fields(alt)
		for i, w := range words {
			f, n := utf8.DecodeRuneInString(w)
			words[i] = string(unicode.ToUpper(f)) + w[n:]
		}
		return strings.Join(words, " ")
	}
	return alt
}
