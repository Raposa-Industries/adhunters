package classify

// Text features, ported from adhunters-collector e20148c
// (internal/vertical/text.go).

import (
	"math"
	"strings"
	"unicode"
)

// Doc is the text of one creative, in two parts. The classifier keeps the
// parts apart, so a word in a headline and the same word on a landing page are
// two features.
type Doc struct {
	Ad   string // headlines and descriptions of its ads
	Page string // landing page title, headings and text, and the next funnel step
}

// stopwords are left out of unigrams and skipped when building bigrams.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an and are as at be been but by can could did do does for from
		had has have he her here his how i if in into is it its just me more most my no not now of on
		one or our out so some than that the their them then there these they this those to too up us
		was we were what when where which who why will with would you your yours about after all also
		am any because before being both each few further get got off once only other own same should
		very s t don won ll re ve d m`) {
		m[w] = true
	}
	return m
}()

// tokens cuts text into lowercase words of letters and digits. Apostrophes
// inside a word are dropped ("women's" -> "womens"), hyphens split words.
func tokens(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case r == '\'' || r == '’' || r == '‘':
			// part of the word, left out
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// keyword is one keyword or hint of verticals.yaml, cut into words as tokens() cuts text.
type keyword struct {
	words  []string
	prefix bool // the last word ends in *: any word starting with it
}

// keywords are all keywords, found by their first word.
type keywords struct {
	byFirst     map[string][]keyword // first word exact (or plural, for one-word keywords)
	prefixFirst []keyword            // one-word keywords ending in *
}

func parseKeywords(list []string) *keywords {
	kw := &keywords{byFirst: map[string][]keyword{}}
	for _, k := range list {
		prefix := strings.HasSuffix(k, "*")
		w := tokens(strings.TrimSuffix(k, "*"))
		if len(w) == 0 {
			continue
		}
		one := keyword{words: w, prefix: prefix}
		if prefix && len(w) == 1 {
			kw.prefixFirst = append(kw.prefixFirst, one)
		} else {
			kw.byFirst[w[0]] = append(kw.byFirst[w[0]], one)
		}
	}
	return kw
}

func wordMatches(tok, kw string, prefix bool) bool {
	if prefix {
		return strings.HasPrefix(tok, kw)
	}
	return tok == kw || tok == kw+"s" || tok == kw+"es"
}

// matchAt returns how many tokens from i on k covers, or 0.
func (k keyword) matchAt(toks []string, i int) int {
	n := len(k.words)
	if i+n > len(toks) {
		return 0
	}
	for j := 0; j < n; j++ {
		if j == n-1 {
			if !wordMatches(toks[i+j], k.words[j], k.prefix) {
				return 0
			}
		} else if toks[i+j] != k.words[j] {
			return 0
		}
	}
	return n
}

// maskKeywords removes every keyword the rules know from toks. The classifier
// trains on masked copies too, so it learns the other words that go with each
// vertical: exactly what is left on an ad the rules are unsure about.
func maskKeywords(toks []string, kws *keywords) []string {
	if kws == nil {
		return toks
	}
	drop := make([]bool, len(toks))
	mark := func(i, n int) {
		for j := 0; j < n; j++ {
			drop[i+j] = true
		}
	}
	for i, t := range toks {
		// A one-word keyword may match in plural, so look up the singular too.
		for _, first := range []string{t, strings.TrimSuffix(t, "s"), strings.TrimSuffix(t, "es")} {
			for _, k := range kws.byFirst[first] {
				mark(i, k.matchAt(toks, i))
			}
			if !strings.HasSuffix(t, "s") {
				break
			}
		}
		for _, k := range kws.prefixFirst {
			mark(i, k.matchAt(toks, i))
		}
	}
	out := toks[:0:0]
	for i, t := range toks {
		if !drop[i] {
			out = append(out, t)
		}
	}
	return out
}

// terms are the unigrams and bigrams of one part, stopwords left out, each
// with the part's prefix ("a:" ad, "p:" page) and its count.
func terms(prefix string, toks []string, into map[string]float32) {
	prev := ""
	for _, t := range toks {
		if stopwords[t] || len(t) < 2 {
			continue
		}
		into[prefix+t]++
		if prev != "" {
			into[prefix+prev+"_"+t]++
		}
		prev = t
	}
}

// docTerms are the terms of both parts. With kws set, keywords are masked first.
func docTerms(d Doc, kws *keywords) map[string]float32 {
	m := map[string]float32{}
	ad, page := tokens(d.Ad), tokens(d.Page)
	if kws != nil {
		ad, page = maskKeywords(ad, kws), maskKeywords(page, kws)
	}
	terms("a:", ad, m)
	terms("p:", page, m)
	return m
}

// sparse is a feature vector: column indexes and values.
type sparse struct {
	idx []int32
	val []float32
}

// vectorize turns terms into a feature vector: 1 + log(count) per known term,
// each part scaled to length 1, so a long page does not drown the headline.
func vectorize(m map[string]float32, vocab map[string]int32) sparse {
	var v sparse
	var isAd []bool
	var norm [2]float64 // ad, page
	for t, c := range m {
		col, ok := vocab[t]
		if !ok {
			continue
		}
		x := float32(1 + math.Log(float64(c)))
		ad := t[0] == 'a'
		v.idx = append(v.idx, col)
		v.val = append(v.val, x)
		isAd = append(isAd, ad)
		if ad {
			norm[0] += float64(x * x)
		} else {
			norm[1] += float64(x * x)
		}
	}
	na, np := float32(math.Sqrt(norm[0])), float32(math.Sqrt(norm[1]))
	for k := range v.idx {
		if isAd[k] {
			v.val[k] /= na
		} else {
			v.val[k] /= np
		}
	}
	return v
}
