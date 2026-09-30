package classify

// The classifier model, ported from adhunters-collector e20148c
// (internal/vertical/softmax.go), with our vertical ids as its classes.

import (
	"bytes"
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// Model is a multinomial logistic regression (softmax) over word unigrams and
// bigrams. Plain Go, no paid API: training takes seconds, one prediction
// microseconds. It learns from creatives the keyword rules are already sure
// about (weak labels), with the keywords themselves masked out half the time,
// so it also learns the other words that go with each vertical.
type Model struct {
	Labels      []string // vertical id of each class
	Terms       []string // feature of each column
	W           []float32
	B           []float32
	Temperature float32 // divides the scores before softmax; set on held-out creatives

	vocab map[string]int32
}

// Example is one creative the rules are sure about, with their vertical.
type Example struct {
	CreativeID int32
	Doc        Doc
	Label      string
}

// TrainOptions tune training. Zero values take the defaults.
type TrainOptions struct {
	MaxFeatures int     // most common terms kept, default 30,000
	MinDocs     int     // a term must be in this many creatives, default 3
	Epochs      int     // default 8
	Rate        float32 // start learning rate, default 0.5
	L2          float32 // weight decay per update, default 1e-6
	MaskShare   float64 // share of training passes with keywords masked, default 0.5
	TestShare   float64 // held out for the numbers in Eval, default 0.15
	CalibShare  float64 // held out to set the temperature, default 0.1
	Seed        int64
}

func (o *TrainOptions) defaults() {
	if o.MaxFeatures == 0 {
		o.MaxFeatures = 30000
	}
	if o.MinDocs == 0 {
		o.MinDocs = 3
	}
	if o.Epochs == 0 {
		o.Epochs = 8
	}
	if o.Rate == 0 {
		o.Rate = 0.5
	}
	if o.L2 == 0 {
		o.L2 = 1e-6
	}
	if o.MaskShare == 0 {
		o.MaskShare = 0.5
	}
	if o.TestShare == 0 {
		o.TestShare = 0.15
	}
	if o.CalibShare == 0 {
		o.CalibShare = 0.1
	}
	if o.Seed == 0 {
		o.Seed = 1
	}
}

// Eval is how the model did on held-out creatives the rules are sure about.
// "Masked" is the same creatives with every keyword removed: closer to what an
// unsure creative looks like, and so the more honest number.
type Eval struct {
	Train                  int     `json:"train"`
	Test                   int     `json:"test"`
	Accuracy               float64 `json:"accuracy"`
	MaskedAccuracy         float64 `json:"masked_accuracy"`
	CategoryAccuracy       float64 `json:"category_accuracy"` // the right category, whatever the vertical
	MaskedCategoryAccuracy float64 `json:"masked_category_accuracy"`
	// Share of held-out creatives the model is sure about (>= 0.6), and how
	// many of those it gets right. Masked copies.
	MaskedSureShare    float64              `json:"masked_sure_share"`
	MaskedSureAccuracy float64              `json:"masked_sure_accuracy"`
	PerVertical        map[string]ClassEval `json:"per_vertical"`
}

type ClassEval struct {
	N              int     `json:"n"`
	Accuracy       float64 `json:"accuracy"`
	MaskedAccuracy float64 `json:"masked_accuracy"`
}

// encoded is one example, as feature vectors with and without keywords.
type encoded struct {
	label  int
	full   sparse
	masked sparse
}

// Train fits a model. kws are the keywords of every vertical (masked out
// in half the passes). categoryOf gives each vertical its category, for Eval.
func Train(examples []Example, kws *keywords, categoryOf map[string]string, opt TrainOptions) (*Model, Eval, error) {
	opt.defaults()
	rng := rand.New(rand.NewSource(opt.Seed))

	// Classes.
	classOf := map[string]int{}
	m := &Model{}
	for _, e := range examples {
		if _, ok := classOf[e.Label]; !ok {
			classOf[e.Label] = len(m.Labels)
			m.Labels = append(m.Labels, e.Label)
		}
	}
	if len(m.Labels) < 2 {
		return nil, Eval{}, fmt.Errorf("need at least 2 verticals with sure creatives, have %d", len(m.Labels))
	}
	k := len(m.Labels)

	// Split: test, calibration, train.
	order := rng.Perm(len(examples))
	nTest := int(float64(len(examples)) * opt.TestShare)
	nCalib := int(float64(len(examples)) * opt.CalibShare)
	testIdx, calibIdx, trainIdx := order[:nTest], order[nTest:nTest+nCalib], order[nTest+nCalib:]

	// Vocabulary from the training part only. Terms are counted and thrown
	// away, then made again per example below: holding every example's terms
	// at once takes gigabytes.
	df := map[string]int{}
	for _, i := range trainIdx {
		for t := range docTerms(examples[i].Doc, nil) {
			df[t]++
		}
	}
	for t, n := range df {
		if n < opt.MinDocs {
			delete(df, t)
		}
	}
	m.Terms = make([]string, 0, len(df))
	for t := range df {
		m.Terms = append(m.Terms, t)
	}
	sort.Slice(m.Terms, func(a, b int) bool {
		if df[m.Terms[a]] != df[m.Terms[b]] {
			return df[m.Terms[a]] > df[m.Terms[b]]
		}
		return m.Terms[a] < m.Terms[b]
	})
	if len(m.Terms) > opt.MaxFeatures {
		m.Terms = m.Terms[:opt.MaxFeatures]
	}
	df = nil
	m.index()

	// Each example as two feature vectors: with and without keywords.
	enc := func(idx []int) []encoded {
		out := make([]encoded, len(idx))
		for j, i := range idx {
			out[j] = encoded{
				label:  classOf[examples[i].Label],
				full:   vectorize(docTerms(examples[i].Doc, nil), m.vocab),
				masked: vectorize(docTerms(examples[i].Doc, kws), m.vocab),
			}
		}
		return out
	}
	train, calib, test := enc(trainIdx), enc(calibIdx), enc(testIdx)

	// Stochastic gradient descent on the softmax loss.
	m.W = make([]float32, len(m.Terms)*k)
	m.B = make([]float32, k)
	m.Temperature = 1
	scores := make([]float32, k)
	for epoch := 0; epoch < opt.Epochs; epoch++ {
		rate := opt.Rate / float32(1+epoch)
		for _, j := range rng.Perm(len(train)) {
			e := train[j]
			x := e.full
			if rng.Float64() < opt.MaskShare {
				x = e.masked
			}
			m.probs(x, scores, 1)
			scores[e.label] -= 1 // gradient of the loss per class: p - y
			for c := 0; c < k; c++ {
				m.B[c] -= rate * scores[c]
			}
			for f, col := range x.idx {
				v := x.val[f]
				row := m.W[int(col)*k : int(col)*k+k]
				for c := range row {
					row[c] -= rate * (scores[c]*v + opt.L2*row[c])
				}
			}
		}
	}

	// Temperature: the one that gives the held-out creatives (both copies) the
	// best log likelihood, so "0.6" means right about 60% of the time.
	if len(calib) > 0 {
		var logits [][]float32
		var labels []int
		for _, e := range calib {
			for _, x := range []sparse{e.full, e.masked} {
				l := make([]float32, k)
				m.logits(x, l)
				logits = append(logits, l)
				labels = append(labels, e.label)
			}
		}
		best, bestLoss := float32(1), math.Inf(1)
		for t := float32(0.3); t <= 4.0; t += 0.05 {
			loss := 0.0
			for i, l := range logits {
				copy(scores, l)
				softmax(scores, t)
				loss -= math.Log(math.Max(float64(scores[labels[i]]), 1e-9))
			}
			if loss < bestLoss {
				best, bestLoss = t, loss
			}
		}
		m.Temperature = best
	}

	ev := m.evaluate(test, categoryOf)
	ev.Train = len(train)
	return m, ev, nil
}

func (m *Model) index() {
	m.vocab = make(map[string]int32, len(m.Terms))
	for i, t := range m.Terms {
		m.vocab[t] = int32(i)
	}
}

// probs writes the class probabilities of x into out.
func (m *Model) probs(x sparse, out []float32, temperature float32) {
	m.logits(x, out)
	softmax(out, temperature)
}

// logits writes the raw class scores of x into out.
func (m *Model) logits(x sparse, out []float32) {
	k := len(m.Labels)
	copy(out, m.B)
	for f, col := range x.idx {
		v := x.val[f]
		row := m.W[int(col)*k : int(col)*k+k]
		for c, w := range row {
			out[c] += w * v
		}
	}
}

func softmax(out []float32, temperature float32) {
	top := float32(math.Inf(-1))
	for c := range out {
		out[c] /= temperature
		if out[c] > top {
			top = out[c]
		}
	}
	var sum float32
	for c := range out {
		out[c] = float32(math.Exp(float64(out[c] - top)))
		sum += out[c]
	}
	for c := range out {
		out[c] /= sum
	}
}

// Guess is one vertical and its probability.
type Guess struct {
	Vertical string
	P        float64
}

// Predict returns every vertical, most likely first. ok is false when the
// creative has no known word: then there is nothing to guess from.
func (m *Model) Predict(d Doc) (guesses []Guess, ok bool) {
	x := vectorize(docTerms(d, nil), m.vocab)
	if len(x.idx) == 0 {
		return nil, false
	}
	p := make([]float32, len(m.Labels))
	m.probs(x, p, m.Temperature)
	guesses = make([]Guess, len(p))
	for c := range p {
		guesses[c] = Guess{Vertical: m.Labels[c], P: float64(p[c])}
	}
	sort.Slice(guesses, func(a, b int) bool { return guesses[a].P > guesses[b].P })
	return guesses, true
}

func (m *Model) evaluate(test []encoded, categoryOf map[string]string) Eval {
	ev := Eval{Test: len(test), PerVertical: map[string]ClassEval{}}
	if len(test) == 0 {
		return ev
	}
	k := len(m.Labels)
	p := make([]float32, k)
	argmax := func() (int, float32) {
		best := 0
		for c := 1; c < k; c++ {
			if p[c] > p[best] {
				best = c
			}
		}
		return best, p[best]
	}
	type tally struct{ n, ok, okMasked int }
	per := map[int]*tally{}
	var ok, okMasked, sameCat, sameCatMasked, sure, sureOK int
	for _, e := range test {
		t := per[e.label]
		if t == nil {
			t = &tally{}
			per[e.label] = t
		}
		t.n++
		want := m.Labels[e.label]

		m.probs(e.full, p, m.Temperature)
		c, _ := argmax()
		if c == e.label {
			ok++
			t.ok++
		}
		if categoryOf[m.Labels[c]] == categoryOf[want] {
			sameCat++
		}

		m.probs(e.masked, p, m.Temperature)
		c, pc := argmax()
		if c == e.label {
			okMasked++
			t.okMasked++
		}
		if categoryOf[m.Labels[c]] == categoryOf[want] {
			sameCatMasked++
		}
		if pc >= 0.6 {
			sure++
			if c == e.label {
				sureOK++
			}
		}
	}
	n := float64(len(test))
	ev.Accuracy = float64(ok) / n
	ev.MaskedAccuracy = float64(okMasked) / n
	ev.CategoryAccuracy = float64(sameCat) / n
	ev.MaskedCategoryAccuracy = float64(sameCatMasked) / n
	ev.MaskedSureShare = float64(sure) / n
	if sure > 0 {
		ev.MaskedSureAccuracy = float64(sureOK) / float64(sure)
	}
	for c, t := range per {
		ev.PerVertical[m.Labels[c]] = ClassEval{
			N: t.n, Accuracy: float64(t.ok) / float64(t.n), MaskedAccuracy: float64(t.okMasked) / float64(t.n),
		}
	}
	return ev
}

// packed is the stored form: weights as int8, one scale per term, so a model
// of 30,000 terms and 50 verticals is about 1.5 MB instead of 6.
type packed struct {
	Labels      []string
	Terms       []string
	Q           []int8
	Scale       []float32 // per term: weight = Q * Scale
	B           []float32
	Temperature float32
}

// Encode packs the model for spy.class_model.model (int8 weights, gob, gzip).
func (m *Model) Encode() ([]byte, error) {
	k := len(m.Labels)
	p := packed{Labels: m.Labels, Terms: m.Terms, B: m.B, Temperature: m.Temperature,
		Q: make([]int8, len(m.W)), Scale: make([]float32, len(m.Terms))}
	for f := range m.Terms {
		row := m.W[f*k : f*k+k]
		var top float32
		for _, w := range row {
			top = max(top, float32(math.Abs(float64(w))))
		}
		if top == 0 {
			continue
		}
		p.Scale[f] = top / 127
		for c, w := range row {
			p.Q[f*k+c] = int8(math.Round(float64(w / p.Scale[f])))
		}
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err := gob.NewEncoder(zw).Encode(p); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode unpacks a model written by Encode.
func Decode(b []byte) (*Model, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var p packed
	if err := gob.NewDecoder(zr).Decode(&p); err != nil {
		return nil, err
	}
	k := len(p.Labels)
	if k == 0 || len(p.Q) != len(p.Terms)*k || len(p.Scale) != len(p.Terms) || len(p.B) != k {
		return nil, fmt.Errorf("model is damaged: %d classes, %d terms, %d weights", k, len(p.Terms), len(p.Q))
	}
	m := &Model{Labels: p.Labels, Terms: p.Terms, B: p.B, Temperature: p.Temperature, W: make([]float32, len(p.Q))}
	for i, q := range p.Q {
		m.W[i] = float32(q) * p.Scale[i/k]
	}
	m.index()
	return m, nil
}
