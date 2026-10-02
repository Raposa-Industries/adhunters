package store_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"strconv"
	"testing"

	"github.com/Raposa-Industries/adhunters/library/internal/store"
	"github.com/Raposa-Industries/adhunters/library/internal/testdb"
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
	return store.New(testdb.New(t))
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

func TestOpenBeforeAndAfterUpload(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	c, _, err := s.AddCreative(ctx, store.NewCreative{Origin: store.OriginUpload}, pic(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	// Waiting for its upload: the bytes come from the row.
	rc, mt, err := s.Open(ctx, c.ID, false)
	if err != nil || mt != "image/png" {
		t.Fatalf("open waiting: %v %q", err, mt)
	}
	b, _ := io.ReadAll(rc)
	if !bytes.Equal(b, pic(t, 1)) {
		t.Error("waiting bytes differ")
	}
	// In Drive, with no sign-in: the library says so rather than failing blind.
	if _, err := s.DB().Exec(ctx, `UPDATE library.creative SET pending = NULL, drive_state = 'in_drive', drive_file_id = 'f1' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Open(ctx, c.ID, false); !errors.Is(err, store.ErrNoDrive) {
		t.Errorf("open without drive: %v", err)
	}
	s.UseDrive(func(context.Context) (store.Drive, error) { return fakeDrive{"f1": pic(t, 1)}, nil })
	rc, _, err = s.Open(ctx, c.ID, false)
	if err != nil {
		t.Fatalf("open from drive: %v", err)
	}
	if b, _ := io.ReadAll(rc); !bytes.Equal(b, pic(t, 1)) {
		t.Error("drive bytes differ")
	}
	// Its Drive file vanished between passes.
	s.UseDrive(func(context.Context) (store.Drive, error) { return fakeDrive{}, nil })
	if _, _, err := s.Open(ctx, c.ID, false); !errors.Is(err, store.ErrGone) {
		t.Errorf("open a missing drive file: %v", err)
	}
}

func TestTakeFromBucket(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	waiting, _, _ := s.AddCreative(ctx, store.NewCreative{Origin: store.OriginUpload}, pic(t, 1))
	uploaded, _, _ := s.AddCreative(ctx, store.NewCreative{Origin: store.OriginUpload}, pic(t, 2))
	// The third is waiting too, but the bucket never had its files.
	_, _, _ = s.AddCreative(ctx, store.NewCreative{Origin: store.OriginUpload}, pic(t, 3))
	// As the bucket days left them: keys, no bytes in the rows.
	if _, err := s.DB().Exec(ctx, `UPDATE library.creative SET pending = NULL, thumb = NULL,
		file_key = 'f' || id, thumb_key = 't' || id`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(ctx, `UPDATE library.creative SET drive_state = 'in_drive', drive_file_id = 'd' WHERE id = $1`, uploaded.ID); err != nil {
		t.Fatal(err)
	}
	bucket := map[string][]byte{
		key("f", waiting.ID): pic(t, 1), key("t", waiting.ID): []byte("thumb1"),
		key("f", uploaded.ID): pic(t, 2), key("t", uploaded.ID): []byte("thumb2"),
	}
	get := func(_ context.Context, k string) (io.ReadCloser, error) {
		b, ok := bucket[k]
		if !ok {
			return nil, errors.New("no such key")
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	n, errs := s.TakeFromBucket(ctx, get)
	if n != 2 || len(errs) != 2 {
		t.Fatalf("moved %d, errors %v", n, errs)
	}
	var pending []byte
	_ = s.DB().QueryRow(ctx, `SELECT pending FROM library.creative WHERE id = $1`, waiting.ID).Scan(&pending)
	if !bytes.Equal(pending, pic(t, 1)) {
		t.Error("the waiting creative did not get its bytes")
	}
	_ = s.DB().QueryRow(ctx, `SELECT pending FROM library.creative WHERE id = $1`, uploaded.ID).Scan(&pending)
	if pending != nil {
		t.Error("an uploaded creative took bytes it does not need")
	}
	rc, _, err := s.Open(ctx, uploaded.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); string(b) != "thumb2" {
		t.Errorf("thumbnail %q", b)
	}
	// Run again: only the one the bucket never had is left to try.
	n, errs = s.TakeFromBucket(ctx, get)
	if n != 0 || len(errs) != 2 {
		t.Errorf("second run moved %d, errors %v", n, errs)
	}
}

func key(prefix string, id int64) string { return prefix + strconv.FormatInt(id, 10) }

type fakeDrive map[string][]byte

func (f fakeDrive) Download(_ context.Context, id string) ([]byte, error) {
	if b, ok := f[id]; ok {
		return b, nil
	}
	return nil, notFound{}
}

type notFound struct{}

func (notFound) Error() string  { return "drive: 404" }
func (notFound) NotFound() bool { return true }

// The platform picks the network letter; the counter stays the vertical's.
func TestPlatformLetter(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, _, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "tinnitus", Origin: store.OriginCreate, Platform: "newsbreak"}, pic(t, 1))
	if err != nil || a.Name != "TINN1" {
		t.Fatalf("newsbreak: %+v %v", a, err)
	}
	b, _, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "tinnitus", Origin: store.OriginCreate, Platform: "taboola"}, pic(t, 2))
	if err != nil || b.Name != "TINT2" {
		t.Fatalf("taboola: %+v %v", b, err)
	}
	var bad store.BadInput
	if _, _, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "tinnitus", Origin: store.OriginCreate, Platform: "outbrain"}, pic(t, 3)); !errors.As(err, &bad) {
		t.Fatalf("unknown platform: %v", err)
	}
	if _, err := s.AddSet(ctx, store.NewSet{Name: "x", VerticalID: "tinnitus", Platform: "outbrain"}); !errors.As(err, &bad) {
		t.Fatalf("unknown set platform: %v", err)
	}
}

// Create's library pages: tags, the original and generated filters, sorting,
// refiling between sets (with a record of where an item was) and the folder
// tree with its counts.
func TestTagsRefileAndFolders(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, err := s.AddSet(ctx, store.NewSet{Name: "Colher", VerticalID: "memory-loss", Origin: store.OriginCreate, Platform: "taboola"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddSet(ctx, store.NewSet{Name: "Sofa", VerticalID: "memory-loss", Origin: store.OriginCreate, Platform: "taboola"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.AddSet(ctx, store.NewSet{Name: "Other", VerticalID: "blood-pressure", Origin: store.OriginCreate})
	if err != nil {
		t.Fatal(err)
	}
	made, _, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "memory-loss", SetID: a.ID, Origin: store.OriginCreate,
		Tags: []string{"#Cozinha", "cozinha", " Colher "}, MadeBy: "mari"}, pic(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(made.Tags) != 2 || made.Tags[0] != "colher" || made.Tags[1] != "cozinha" {
		t.Fatalf("tags = %v", made.Tags)
	}
	up, _, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "memory-loss", SetID: a.ID, Origin: store.OriginUpload}, pic(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	// The same bytes with a new tag: the tag goes on the creative kept.
	again, created, err := s.AddCreative(ctx, store.NewCreative{VerticalID: "memory-loss", Origin: store.OriginCreate, Tags: []string{"mesa"}}, pic(t, 10))
	if err != nil || created || again.ID != made.ID || len(again.Tags) != 3 {
		t.Fatalf("again = %+v created=%v err=%v", again, created, err)
	}
	if _, err := s.AddHeadlines(ctx, []store.NewHeadline{{Text: "A Calm Morning Habit", VerticalID: "memory-loss", SetID: a.ID,
		Origin: store.OriginCreate, Tags: []string{"Manhã"}}}); err != nil {
		t.Fatal(err)
	}

	// Originals are everything not made in Create.
	orig, err := s.Creatives(ctx, store.Filter{Origin: "upload,drive"})
	if err != nil || len(orig) != 1 || orig[0].ID != up.ID {
		t.Fatalf("originals = %+v %v", orig, err)
	}
	gen, _ := s.Creatives(ctx, store.Filter{Origin: "create"})
	if len(gen) != 1 || gen[0].ID != made.ID {
		t.Fatalf("generated = %+v", gen)
	}
	// A tag filters and is found by the search.
	if l, _ := s.Creatives(ctx, store.Filter{Tag: "Cozinha"}); len(l) != 1 || l[0].ID != made.ID {
		t.Fatalf("tag filter = %+v", l)
	}
	if l, _ := s.Creatives(ctx, store.Filter{Search: "mes"}); len(l) != 1 {
		t.Fatalf("search by tag = %+v", l)
	}
	if l, _ := s.Headlines(ctx, store.Filter{Search: "manhã"}); len(l) != 1 || l[0].Tags[0] != "manhã" {
		t.Fatalf("headline tag = %+v", l)
	}
	// Oldest first, and the platform's folder.
	if l, _ := s.Creatives(ctx, store.Filter{Sort: "old"}); len(l) != 2 || l[0].ID != made.ID {
		t.Fatalf("oldest first = %+v", l)
	}
	if l, _ := s.Creatives(ctx, store.Filter{VerticalID: "memory-loss", Platform: "newsbreak"}); len(l) != 0 {
		t.Fatalf("newsbreak = %+v", l)
	}

	// Refile: out of a, into b, with a record; another vertical's set is refused.
	moved, err := s.ChangeCreative(ctx, made.ID, store.Change{RefileTo: b.ID, By: "mari", RemoveTags: []string{"mesa"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved.SetIDs) != 1 || moved.SetIDs[0] != b.ID || len(moved.Tags) != 2 {
		t.Fatalf("refiled = %+v", moved)
	}
	var from []int64
	if err := s.DB().QueryRow(ctx, `SELECT from_sets FROM library.refile WHERE kind = 'creative' AND item_id = $1`, made.ID).Scan(&from); err != nil ||
		len(from) != 1 || from[0] != a.ID {
		t.Fatalf("refile record = %v %v", from, err)
	}
	if _, err := s.ChangeCreative(ctx, made.ID, store.Change{RefileTo: other.ID}); err == nil {
		t.Fatal("refiled into another vertical's set")
	}
	hidden := true
	if _, err := s.ChangeCreative(ctx, up.ID, store.Change{Hidden: &hidden}); err != nil {
		t.Fatal(err)
	}

	f, err := s.Folders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.Totals.Creatives != 1 || f.Totals.Generated != 1 || f.Totals.Original != 0 || f.Totals.Headlines != 1 {
		t.Fatalf("totals = %+v", f.Totals)
	}
	var ml *store.VerticalFolder
	for i := range f.Verticals {
		if f.Verticals[i].ID == "memory-loss" {
			ml = &f.Verticals[i]
		}
	}
	if ml == nil || ml.Creatives != 1 || len(ml.Platforms) != 1 || ml.Platforms[0].Name != "Taboola" || ml.Platforms[0].Creatives != 1 ||
		len(ml.Sets) != 2 || ml.Sets[0].ID != a.ID || ml.Sets[0].Creatives != 0 || ml.Sets[1].Creatives != 1 {
		t.Fatalf("memory loss folder = %+v", ml)
	}
	tags, err := s.Tags(ctx, "memory-loss")
	if err != nil || len(tags) != 3 {
		t.Fatalf("tags = %+v %v", tags, err)
	}
}
