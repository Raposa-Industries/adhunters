package store_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"

	"github.com/Raposa-Industries/adhunters/library/internal/store"
	"github.com/Raposa-Industries/adhunters/library/internal/testdb"
	"github.com/Raposa-Industries/adhunters/shared/files"
)

// pic is a small PNG; seed makes each one's bytes different.
func pic(t *testing.T, seed uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 120, 68))
	img.Set(1, 1, color.RGBA{seed, 2, 3, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func newStore(t *testing.T) *store.Store {
	return store.New(testdb.New(t), &files.Dir{Root: t.TempDir()})
}

func TestCreativesAreMintedAndFoundByBytes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	set, err := s.AddSet(ctx, store.NewSet{Name: "BP · Colher · 29 Sep", VerticalID: "blood-pressure", Origin: store.OriginCreate, MadeBy: "mari"})
	if err != nil {
		t.Fatal(err)
	}
	a, created, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "blood-pressure", SetID: set.ID, Angle: "Colher",
		Origin: store.OriginCreate, AILabel: store.AIYes, Idea: "Spoon over the mug"}, pic(t, 1))
	if err != nil || !created {
		t.Fatalf("add: %v created=%v", err, created)
	}
	if a.Name != "BPT1" || a.Width != 120 || a.MediaType != "image/png" || len(a.SetIDs) != 1 {
		t.Fatalf("creative = %+v", a)
	}
	b, _, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "blood-pressure", Origin: store.OriginCreate}, pic(t, 2))
	if err != nil || b.Name != "BPT2" {
		t.Fatalf("second: %+v %v", b, err)
	}
	// The same bytes again: the same creative, no new number.
	again, created, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "blood-pressure", Origin: store.OriginUpload}, pic(t, 1))
	if err != nil || created || again.ID != a.ID {
		t.Fatalf("again: %+v created=%v err=%v", again, created, err)
	}
	c, _, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "blood-pressure", Origin: store.OriginCreate}, pic(t, 3))
	if err != nil || c.Name != "BPT3" {
		t.Fatalf("third: %+v %v", c, err)
	}

	// A new vertical gets a code from its initials.
	h, _, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "hair-loss", Origin: store.OriginCreate}, pic(t, 4))
	if err != nil || h.Name != "HLT1" {
		t.Fatalf("new vertical: %+v %v", h, err)
	}

	list, err := s.Creatives(ctx, store.Filter{SetID: set.ID})
	if err != nil || len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("set list = %+v %v", list, err)
	}
	hidden := true
	if _, err := s.ChangeCreative(ctx, b.ID, store.Change{Hidden: &hidden}); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Creatives(ctx, store.Filter{VerticalID: "blood-pressure"})
	if len(list) != 2 {
		t.Fatalf("hidden one still listed: %d", len(list))
	}
	list, _ = s.Creatives(ctx, store.Filter{Search: "spoon"})
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("search = %+v", list)
	}

	rc, mt, err := s.Open(ctx, a.ID, false)
	if err != nil || mt != "image/png" {
		t.Fatal(err, mt)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, pic(t, 1)) {
		t.Error("the safe copy is not the bytes saved")
	}
	rc, mt, err = s.Open(ctx, a.ID, true)
	if err != nil || mt != "image/jpeg" {
		t.Fatal(err, mt)
	}
	_ = rc.Close()

	if _, _, err := s.AddCreative(ctx, store.NewCreative{}, []byte("not a picture")); !isBad(err) {
		t.Errorf("a non-picture: %v", err)
	}
}

func TestHeadlinesAndSets(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	set, _ := s.AddSet(ctx, store.NewSet{Name: "Memory set", VerticalID: "memory-loss"})
	dup, err := s.AddSet(ctx, store.NewSet{Name: "memory set", VerticalID: "memory-loss"})
	if err != nil || dup.Name != "memory set (2)" {
		t.Fatalf("same name: %+v %v", dup, err)
	}
	hs, err := s.AddHeadlines(ctx, []store.NewHeadline{
		{Text: "Doctors\u200b Reveal 4 Drinks", VerticalID: "memory-loss", SetID: set.ID, Origin: store.OriginCreate},
		{Text: "  Doctors Reveal 4 Drinks ", SetID: dup.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hs[0].ID != hs[1].ID || hs[0].Text != "Doctors Reveal 4 Drinks" || len(hs[1].SetIDs) != 2 {
		t.Fatalf("headlines = %+v", hs)
	}
	sets, err := s.Sets(ctx, "memory-loss", 0)
	if err != nil || len(sets) != 2 || sets[0].Headlines != 1 {
		t.Fatalf("sets = %+v %v", sets, err)
	}
	if _, err := s.AddHeadlines(ctx, []store.NewHeadline{{Text: "\u200b"}}); !isBad(err) {
		t.Errorf("empty headline: %v", err)
	}
	if _, err := s.AddHeadlines(ctx, []store.NewHeadline{{Text: "x", SetID: 999}}); !isBad(err) {
		t.Errorf("missing set: %v", err)
	}
	ai := store.AINo
	h, err := s.ChangeHeadline(ctx, hs[0].ID, store.Change{AILabel: &ai})
	if err != nil || h.AILabel != store.AINo {
		t.Fatalf("change: %+v %v", h, err)
	}
}

func TestChangeVertical(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	code, next := "BPX", 40
	v, err := s.ChangeVertical(ctx, "blood-pressure", store.VerticalChange{Code: &code, NextNumber: &next})
	if err != nil || v.Code != "BPX" || v.NextNumber != 40 {
		t.Fatalf("%+v %v", v, err)
	}
	back := 3
	v, _ = s.ChangeVertical(ctx, "blood-pressure", store.VerticalChange{NextNumber: &back})
	if v.NextNumber != 40 {
		t.Errorf("next_number went down to %d", v.NextNumber)
	}
	taken := "MM"
	if _, err := s.ChangeVertical(ctx, "blood-pressure", store.VerticalChange{Code: &taken}); !isBad(err) {
		t.Errorf("a taken code: %v", err)
	}
	c, _, _ := s.AddCreative(ctx, store.NewCreative{VerticalID: "blood-pressure", Origin: store.OriginCreate}, pic(t, 9))
	if c.Name != "BPXT40" {
		t.Errorf("name %q", c.Name)
	}
}

func isBad(err error) bool {
	var b store.BadInput
	return errors.As(err, &b)
}
