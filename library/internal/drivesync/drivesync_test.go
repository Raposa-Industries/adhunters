package drivesync_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/Raposa-Industries/adhunters/library/internal/drive"
	"github.com/Raposa-Industries/adhunters/library/internal/drive/drivetest"
	"github.com/Raposa-Industries/adhunters/library/internal/drivesync"
	"github.com/Raposa-Industries/adhunters/library/internal/store"
	"github.com/Raposa-Industries/adhunters/library/internal/testdb"
)

func pic(t *testing.T, seed uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 60, 34))
	img.Set(0, 0, color.RGBA{seed, 1, 1, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSync(t *testing.T) {
	ctx := context.Background()
	fake := drivetest.New(t)
	st := store.New(testdb.New(t))
	dc := drive.New(fake.App(), "refresh")
	st.UseDrive(func(context.Context) (store.Drive, error) { return dc, nil })
	sy := drivesync.New(st, dc, drivetest.Root, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Saved in Create: one creative and two headlines in a set.
	set, err := st.AddSet(ctx, store.NewSet{Name: "BP · Colher · 29 Sep", VerticalID: "blood-pressure", Origin: store.OriginCreate})
	if err != nil {
		t.Fatal(err)
	}
	made, _, err := st.AddCreative(ctx, store.NewCreative{VerticalID: "blood-pressure", SetID: set.ID, Origin: store.OriginCreate}, pic(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddHeadlines(ctx, []store.NewHeadline{{Text: "One", SetID: set.ID}, {Text: "Two", SetID: set.ID}}); err != nil {
		t.Fatal(err)
	}

	// The team's own file in a folder of theirs, and a text file.
	mine := fake.Add(drivetest.Root, "Memory Loss", "application/vnd.google-apps.folder", nil)
	sub := fake.Add(mine, "Old winners", "application/vnd.google-apps.folder", nil)
	theirs := fake.Add(sub, "MMT48.jpg", "image/png", pic(t, 2))
	fake.Add(drivetest.Root, "notes.txt", "text/plain", []byte("hi"))

	res, err := sy.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 2 || res.Added != 1 || res.Gone != 0 || len(res.Pages) == 0 {
		t.Fatalf("first pass %+v", res)
	}

	// Ours went to Blood Pressure/<set>/BPT1.png, with our id on it.
	up := fake.Find("BPT1.png")
	if len(up) != 1 || up[0].AppProperties[drivesync.PropID] != "1" || !bytes.Equal(up[0].Data, pic(t, 1)) {
		t.Fatalf("uploaded %+v", up)
	}
	folder, _ := fake.Get(up[0].Parents[0])
	vfolder, _ := fake.Get(folder.Parents[0])
	if folder.Name != set.Name || vfolder.Name != "Blood Pressure" {
		t.Errorf("uploaded into %q in %q", folder.Name, vfolder.Name)
	}
	hl := fake.Find(drivesync.HeadlinesFile)
	if len(hl) != 1 || string(hl[0].Data) != "One\nTwo\n" {
		t.Fatalf("headlines file %+v", hl)
	}
	made, _ = st.Creative(ctx, made.ID)
	if made.DriveState != "in_drive" {
		t.Errorf("drive state %q", made.DriveState)
	}
	// Uploaded: Drive holds the bytes and the row no longer does.
	if n := pendingRows(t, st); n != 0 {
		t.Errorf("%d rows still hold bytes after the upload", n)
	}
	if b := read(t, st, made.ID, false); !bytes.Equal(b, pic(t, 1)) {
		t.Error("the picture read back from Drive is not what was saved")
	}
	if b := read(t, st, made.ID, true); len(b) == 0 {
		t.Error("no thumbnail")
	}
	var pages int
	_ = st.DB().QueryRow(ctx, `SELECT count(*) FROM library.drive_page`).Scan(&pages)
	if pages == 0 {
		t.Error("no raw listing pages kept")
	}

	// Theirs came in as a creative of Memory Loss, in a set named by its
	// folder, keeping its own name, and it now carries our id.
	got, err := st.Creatives(ctx, store.Filter{VerticalID: "memory-loss"})
	if err != nil || len(got) != 1 || got[0].Name != "MMT48" || got[0].Origin != store.OriginDrive || len(got[0].SetIDs) != 1 {
		t.Fatalf("imported %+v %v", got, err)
	}
	sets, _ := st.Sets(ctx, "memory-loss", 0)
	if len(sets) != 1 || sets[0].Name != "Old winners" {
		t.Errorf("sets %+v", sets)
	}
	if f, _ := fake.Get(theirs); f.AppProperties[drivesync.PropID] == "" {
		t.Error("their file was not labelled")
	}

	// A second pass reads nothing again and writes nothing again.
	res, err = sy.Run(ctx)
	if err != nil || res.Written != 0 || res.Added != 0 || res.Gone != 0 {
		t.Fatalf("second pass %+v %v", res, err)
	}
	if n := fake.Calls["GET /drive/v3/files/"+theirs]; n != 1 {
		t.Errorf("their file was downloaded %d times", n)
	}

	// Someone deletes our file in Drive, and adds a headline in Create.
	fake.Remove(up[0].ID)
	if _, err := st.AddHeadlines(ctx, []store.NewHeadline{{Text: "Three", SetID: set.ID}}); err != nil {
		t.Fatal(err)
	}
	res, err = sy.Run(ctx)
	if err != nil || res.Gone != 1 || res.Written != 1 {
		t.Fatalf("third pass %+v %v", res, err)
	}
	made, _ = st.Creative(ctx, made.ID)
	if made.DriveState != "gone" {
		t.Errorf("drive state after delete %q", made.DriveState)
	}
	if _, _, err := st.Open(ctx, made.ID, false); !errors.Is(err, store.ErrGone) {
		t.Errorf("open after delete: %v", err)
	}
	if b := read(t, st, made.ID, true); len(b) == 0 {
		t.Error("thumbnail lost with the file")
	}
	// It leaves the lists and the counts.
	if l, _ := st.Creatives(ctx, store.Filter{VerticalID: "blood-pressure"}); len(l) != 0 {
		t.Errorf("deleted picture still listed: %+v", l)
	}
	if l, _ := st.Creatives(ctx, store.Filter{SetID: set.ID}); len(l) != 0 {
		t.Errorf("deleted picture still in its set: %+v", l)
	}
	if f, _ := st.Folders(ctx); f.Totals.Creatives != 1 {
		t.Errorf("counts after delete %+v", f.Totals)
	}
	if hl := fake.Find(drivesync.HeadlinesFile); len(hl) != 1 || !strings.HasSuffix(string(hl[0].Data), "Three\n") {
		t.Errorf("headlines not rewritten: %+v", hl)
	}

	// The bytes of an app's creative put in Drive by hand count as its copy.
	later, _, _ := st.AddCreative(ctx, store.NewCreative{VerticalID: "tinnitus", Origin: store.OriginCreate}, pic(t, 3))
	fake.Add(drivetest.Root, "copy.png", "image/png", pic(t, 3))
	res, err = sy.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	later, _ = st.Creative(ctx, later.ID)
	if later.DriveState != "in_drive" || pendingRows(t, st) != 0 {
		t.Errorf("hand copy: %+v %+v", later, res)
	}

	// Saving the deleted picture again brings it back, uploaded anew.
	back, created, err := st.AddCreative(ctx, store.NewCreative{VerticalID: "blood-pressure", Origin: store.OriginCreate}, pic(t, 1))
	if err != nil || created || back.ID != made.ID || back.DriveState != "waiting" {
		t.Fatalf("saved again: %+v created=%v %v", back, created, err)
	}
	if res, err = sy.Run(ctx); err != nil || res.Written != 1 {
		t.Fatalf("fifth pass %+v %v", res, err)
	}
	if b := read(t, st, made.ID, false); !bytes.Equal(b, pic(t, 1)) {
		t.Error("the picture saved again is not in Drive")
	}

	var runs int
	_ = st.DB().QueryRow(ctx, `SELECT count(*) FROM library.drive_run WHERE finished_at IS NOT NULL AND error = ''`).Scan(&runs)
	if runs != 5 {
		t.Errorf("%d finished runs", runs)
	}
}

func TestSignedOutStopsThePass(t *testing.T) {
	ctx := context.Background()
	fake := drivetest.New(t)
	st := store.New(testdb.New(t))
	sy := drivesync.New(st, drive.New(fake.App(), "revoked"), drivetest.Root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, _, err := st.AddCreative(ctx, store.NewCreative{Origin: store.OriginUpload}, pic(t, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := sy.Run(ctx); err == nil {
		t.Fatal("no error when signed out")
	}
	var msg string
	_ = st.DB().QueryRow(ctx, `SELECT error FROM library.drive_run`).Scan(&msg)
	if !strings.Contains(msg, "drive-login") {
		t.Errorf("run error %q", msg)
	}
}

func pendingRows(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(context.Background(), `SELECT count(*) FROM library.creative WHERE pending IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func read(t *testing.T, st *store.Store, id int64, thumb bool) []byte {
	t.Helper()
	rc, _, err := st.Open(context.Background(), id, thumb)
	if err != nil {
		t.Fatalf("open %d thumb=%v: %v", id, thumb, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRenamedSetRenamesItsFolder(t *testing.T) {
	ctx := context.Background()
	fake := drivetest.New(t)
	st := store.New(testdb.New(t))
	sy := drivesync.New(st, drive.New(fake.App(), "refresh"), drivetest.Root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	set, _ := st.AddSet(ctx, store.NewSet{Name: "Colher", VerticalID: "tinnitus", Origin: store.OriginCreate})
	other, _ := st.AddSet(ctx, store.NewSet{Name: "Copo", VerticalID: "tinnitus", Origin: store.OriginCreate})
	if _, _, err := st.AddCreative(ctx, store.NewCreative{VerticalID: "tinnitus", SetID: set.ID, Origin: store.OriginCreate}, pic(t, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RenameSet(ctx, set.ID, other.Name); err == nil {
		t.Error("renamed onto another set's name")
	}
	got, err := st.RenameSet(ctx, set.ID, "Colher de sopa")
	if err != nil || got.Name != "Colher de sopa" {
		t.Fatalf("rename: %+v %v", got, err)
	}
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	up := fake.Find("TINT1.png")
	if len(up) != 1 {
		t.Fatalf("uploaded %+v", up)
	}
	if f, _ := fake.Get(up[0].Parents[0]); f.Name != "Colher de sopa" {
		t.Errorf("folder is %q", f.Name)
	}
	if len(fake.Find("Colher")) != 0 {
		t.Error("the old folder is still there")
	}
	// Nothing more to rename on the next pass.
	if res, err := sy.Run(ctx); err != nil || res.Written != 0 {
		t.Errorf("third pass %+v %v", res, err)
	}
}

// A set made with a platform goes in <vertical>/<platform>/<set> and its
// creatives carry the platform's letter; a set made without one keeps
// <vertical>/<set>. A picture a person drops in a set folder under a
// platform's folder joins a set of that platform, never a set named after
// the platform.
func TestPlatformFolders(t *testing.T) {
	ctx := context.Background()
	fake := drivetest.New(t)
	st := store.New(testdb.New(t))
	sy := drivesync.New(st, drive.New(fake.App(), "refresh"), drivetest.Root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	nb, err := st.AddSet(ctx, store.NewSet{Name: "Colher", VerticalID: "blood-pressure", Origin: store.OriginCreate, Platform: "newsbreak"})
	if err != nil || nb.Platform != "newsbreak" {
		t.Fatalf("set: %+v %v", nb, err)
	}
	old, _ := st.AddSet(ctx, store.NewSet{Name: "Copo", VerticalID: "blood-pressure", Origin: store.OriginCreate})
	a, _, err := st.AddCreative(ctx, store.NewCreative{VerticalID: "blood-pressure", SetID: nb.ID, Origin: store.OriginCreate}, pic(t, 1))
	if err != nil || a.Name != "BPN1" {
		t.Fatalf("newsbreak creative: %+v %v", a, err)
	}
	b, _, err := st.AddCreative(ctx, store.NewCreative{VerticalID: "blood-pressure", SetID: old.ID, Origin: store.OriginCreate}, pic(t, 2))
	if err != nil || b.Name != "BPT2" {
		t.Fatalf("old set's creative: %+v %v", b, err)
	}
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	where := func(name string) string {
		up := fake.Find(name)
		if len(up) != 1 {
			t.Fatalf("%s uploaded %d times", name, len(up))
		}
		var parts []string
		for id := up[0].Parents[0]; id != drivetest.Root; {
			f, _ := fake.Get(id)
			parts = append([]string{f.Name}, parts...)
			id = f.Parents[0]
		}
		return strings.Join(parts, "/")
	}
	if p := where("BPN1.png"); p != "Blood Pressure/NewsBreak/Colher" {
		t.Errorf("newsbreak set's creative is in %q", p)
	}
	if p := where("BPT2.png"); p != "Blood Pressure/Copo" {
		t.Errorf("old set's creative is in %q", p)
	}

	// A person drops pictures under NewsBreak: one in a new folder, one loose.
	plat := fake.Find("NewsBreak")[0].ID
	mine := fake.Add(plat, "Garrafa", drive.FolderType, nil)
	fake.Add(mine, "hand.png", "image/png", pic(t, 3))
	fake.Add(plat, "loose.png", "image/png", pic(t, 4))
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	sets, err := st.Sets(ctx, "blood-pressure", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range sets {
		got[x.Name] = x.Platform
	}
	if p, ok := got["Garrafa"]; !ok || p != "newsbreak" {
		t.Errorf("hand-made set: %v", got)
	}
	if _, ok := got["NewsBreak"]; ok {
		t.Errorf("the platform's folder became a set: %v", got)
	}
}

func TestTrashedFileLeavesTheLists(t *testing.T) {
	ctx := context.Background()
	fake := drivetest.New(t)
	st := store.New(testdb.New(t))
	dc := drive.New(fake.App(), "refresh")
	st.UseDrive(func(context.Context) (store.Drive, error) { return dc, nil })
	sy := drivesync.New(st, dc, drivetest.Root, slog.New(slog.NewTextHandler(io.Discard, nil)))

	vert := fake.Add(drivetest.Root, "Memory Loss", drive.FolderType, nil)
	set := fake.Add(vert, "Winners", drive.FolderType, nil)
	id := fake.Add(set, "MMT1.png", "image/png", pic(t, 7))
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	listed := func() int {
		l, err := st.Creatives(ctx, store.Filter{VerticalID: "memory-loss"})
		if err != nil {
			t.Fatal(err)
		}
		return len(l)
	}
	if listed() != 1 {
		t.Fatal("not imported")
	}

	// In the trash: gone from the lists.
	fake.Trash(id, true)
	if res, err := sy.Run(ctx); err != nil || res.Gone != 1 {
		t.Fatalf("pass after trash %+v %v", res, err)
	}
	if listed() != 0 {
		t.Error("trashed picture still listed")
	}

	// Taken back out: listed again, without a second download.
	fake.Trash(id, false)
	if res, err := sy.Run(ctx); err != nil || res.Added != 0 {
		t.Fatalf("pass after restore %+v %v", res, err)
	}
	if listed() != 1 {
		t.Error("restored picture not listed")
	}
	if n := fake.Calls["GET /drive/v3/files/"+id]; n != 1 {
		t.Errorf("downloaded %d times", n)
	}
}

func TestTypedHeadlines(t *testing.T) {
	ctx := context.Background()
	fake := drivetest.New(t)
	st := store.New(testdb.New(t))
	dc := drive.New(fake.App(), "refresh")
	st.UseDrive(func(context.Context) (store.Drive, error) { return dc, nil })
	sy := drivesync.New(st, dc, drivetest.Root, slog.New(slog.NewTextHandler(io.Discard, nil)))

	vert := fake.Add(drivetest.Root, "Memory Loss", drive.FolderType, nil)
	folder := fake.Add(vert, "Winners", drive.FolderType, nil)
	fake.Add(folder, "MMT1.png", "image/png", pic(t, 9))
	long := strings.Repeat("x", store.MaxHeadline+1)
	doc := fake.Add(folder, "Headlines", drive.DocType, []byte("\ufeff- One Spoon Trick\r\n2. Doctors Hate This\n\n• One Spoon Trick\n"+long+"\n"))
	fake.Add(vert, "headlines.txt", "text/plain", []byte("Loose Line\n"))

	texts := func(f store.Filter) []string {
		t.Helper()
		hs, err := st.Headlines(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, h := range hs {
			out = append(out, h.Text)
		}
		sort.Strings(out)
		return out
	}
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	sets, _ := st.Sets(ctx, "memory-loss", 0)
	if len(sets) != 1 || sets[0].Name != "Winners" || sets[0].Creatives != 1 || sets[0].Headlines != 2 {
		t.Fatalf("sets %+v", sets)
	}
	set := sets[0].ID
	if got := texts(store.Filter{SetID: set}); strings.Join(got, "|") != "Doctors Hate This|One Spoon Trick" {
		t.Errorf("headlines of the folder %q", got)
	}
	if got := texts(store.Filter{VerticalID: "memory-loss"}); len(got) != 3 {
		t.Errorf("headlines of the vertical %q", got)
	}
	var why string
	_ = st.DB().QueryRow(ctx, `SELECT error FROM library.drive_file WHERE file_id = $1`, doc).Scan(&why)
	if !strings.Contains(why, "[5]") {
		t.Errorf("long line not noted: %q", why)
	}
	var raws int
	_ = st.DB().QueryRow(ctx, `SELECT count(*) FROM library.drive_text`).Scan(&raws)
	if raws != 2 {
		t.Errorf("%d raw texts kept", raws)
	}

	// Unchanged: not read again. Our own Headlines.txt is never read.
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := fake.Calls["GET /drive/v3/files/"+doc+"/export"]; n != 1 {
		t.Errorf("exported %d times", n)
	}
	if mine := fake.Find(drivesync.HeadlinesFile); len(mine) != 1 || fake.Calls["GET /drive/v3/files/"+mine[0].ID] != 0 {
		t.Errorf("our headlines file: %+v", mine)
	}

	// Edited: a line out, a line in.
	fake.Edit(doc, []byte("Doctors Hate This\nThree Foods To Avoid\n"))
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := texts(store.Filter{SetID: set}); strings.Join(got, "|") != "Doctors Hate This|Three Foods To Avoid" {
		t.Errorf("after edit %q", got)
	}

	// Trashed: its headlines leave; back out of the trash, they return.
	fake.Trash(doc, true)
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := texts(store.Filter{SetID: set}); len(got) != 0 {
		t.Errorf("after trash %q", got)
	}
	fake.Trash(doc, false)
	if _, err := sy.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := texts(store.Filter{SetID: set}); strings.Join(got, "|") != "Doctors Hate This|Three Foods To Avoid" {
		t.Errorf("after restore %q", got)
	}
}

func TestHeadlineLines(t *testing.T) {
	got, long := drivesync.HeadlineLines([]byte("1) First\n* Second\n  \n10. Tenth Thing\n3 Foods That Help\nFirst\n"))
	if strings.Join(got, "|") != "First|Second|Tenth Thing|3 Foods That Help" || len(long) != 0 {
		t.Errorf("%q %v", got, long)
	}
}
