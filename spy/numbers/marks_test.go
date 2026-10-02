package numbers

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAccountRoot(t *testing.T) {
	b := newBench(t)
	for id, want := range map[string]string{
		"memo-nb-1-sc":                 "memo-nb",
		"memo-nb-2-sc":                 "memo-nb",
		"memo-nb-sc":                   "memo-nb",
		"acmehealth2-sc":               "acmehealth",
		"hearstmagsus-elle2-sc":        "hearstmagsus",
		"solo-sc":                      "solo",
		"taboolaaccount-mikejones2":    "mikejones",
		"ads-1-sc":                     "ads-1-sc",
		"abc-sc":                       "abc-sc",
		"media-2-sc":                   "media-2-sc",
		"taboolaaccount-joe45gmailcom": "taboolaaccount-joe45gmailcom",
		"best-joe45gmailcom2-sc":       "best-joe45gmailcom2-sc",
		"1234567":                      "1234567",
	} {
		if got := b.text(`SELECT spy.account_root($1)`, id); got != want {
			t.Errorf("account_root(%q) = %q, want %q", id, got, want)
		}
	}
}

// Sub-accounts named alike apart from their numbers are one operator.
func TestGroupingJoinsNumberedSubAccounts(t *testing.T) {
	b := newBench(t)
	b.exec(`INSERT INTO tracks_api.account_v1 (id, external_id) VALUES (1, 'memo-nb-1-sc'), (2, 'memo-nb-2-sc'), (3, 'memo-xy-1-sc')`)
	b.exec(`INSERT INTO spy.site (domain, first_seen_at, last_seen_at) VALUES ('elsewhere.com', now(), now())`)
	b.exec(`UPDATE spy.setting SET text_value = 'grouping' WHERE name = 'operators_from'`)
	if _, err := b.r.Operators(b.ctx); err != nil {
		t.Fatal(err)
	}
	if got := b.text(`SELECT count(DISTINCT operator_id)::text FROM spy.account_operator WHERE account_id IN (1, 2)`); got != "1" {
		t.Errorf("memo-nb-1-sc and memo-nb-2-sc are in %s operators", got)
	}
	if got := b.text(`SELECT reason FROM spy.grouping_member WHERE member = 'account' AND member_id = 2`); got != `same account name root "memo-nb"` {
		t.Errorf("why: %q", got)
	}
	if got := b.text(`SELECT count(*)::text FROM spy.account_operator WHERE account_id = 3`); got != "0" {
		t.Errorf("memo-xy-1-sc, alone, should be no operator yet")
	}
}

func TestMarksAndWatches(t *testing.T) {
	b := newBench(t)
	b.exec(`UPDATE spy.operator SET seller = 'ClickBank memo' WHERE id = 7`)
	b.exec(`INSERT INTO spy.operator (id, name) VALUES (8, 'other · OP8')`)
	b.exec(`INSERT INTO tracks_api.account_v1 (id, external_id) VALUES (500, 'today55-sc'), (501, 'today56-sc')`)
	b.exec(`INSERT INTO spy.account_operator VALUES (500, 7), (501, 8)`)

	b.exec(`SELECT spy_api.mark_operator_v1(7, false, true, '  Memo  ', 'lead')`)
	if got := b.text(`SELECT name || '|' || display_name || '|' || name_is_manual FROM spy.operator WHERE id = 7`); got != "Memo · ClickBank memo · OP7|Memo|true" {
		t.Errorf("nickname: %s", got)
	}
	b.exec(`SELECT spy_api.mark_operator_v1(8, true, false, NULL)`)

	// Rose and scaled after it was watched.
	b.exec(`INSERT INTO spy.direction_event (at, kind, key, from_direction, to_direction, reason, reason_text)
		VALUES ($1, 'operator', '7', 'steady', 'rising', '{}', 'Duas vezes o usual nas últimas 2 horas.')`, now.Add(-time.Hour))
	b.exec(`INSERT INTO spy.size_stats (kind, key, market_kind, market_key, sightings_24h, share_24h_pct, rank_24h,
			sightings_7d, share_7d_pct, rank_7d, share_7d_before_pct, scaled, refreshed_at)
		VALUES ('operator', '7', 'vertical', 'weight-loss', 50, 10, 2, 400, 12.5, 3, 20, true, now())`)

	tg := &sentMessages{}
	r := New(b.db, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Now: func() time.Time { return now }, Telegram: tg, BaseURL: "https://hunt.example/"}, nil)
	if n, err := r.Watches(b.ctx); err != nil || n != 2 {
		t.Fatalf("watches: %d %v", n, err)
	}
	got := tg.texts
	if len(got) != 2 || !strings.HasPrefix(got[0], "👁 <b>Memo está subindo</b>\n") || !strings.HasPrefix(got[1], "👁 <b>Memo escalou</b>\n") ||
		!strings.HasSuffix(got[0], `<a href="https://hunt.example/spy/operators/7">Abrir no Spy</a>`) {
		t.Errorf("sent: %q", got)
	}
	if s := b.text(`SELECT string_agg(status, ' ' ORDER BY id) FROM spy.watch_notice`); s != "sent sent" {
		t.Errorf("statuses: %s", s)
	}
	// Nothing new: nothing sent again.
	if n, err := r.Watches(b.ctx); err != nil || n != 0 || len(got) != 2 {
		t.Errorf("again: %d %v, %d sent", n, err, len(got))
	}
	// Without Telegram a notice shows only in the pages.
	b.exec(`INSERT INTO spy.direction_event (at, kind, key, from_direction, to_direction, reason, reason_text)
		VALUES ($1, 'operator', '7', 'fading', 'rising', '{}', 'De novo.')`, now.Add(-10*time.Minute))
	if _, err := b.r.Watches(b.ctx); err != nil {
		t.Fatal(err)
	}
	if s := b.text(`SELECT status || ': ' || error FROM spy.watch_notice ORDER BY id DESC LIMIT 1`); s != "skipped: no Telegram bot token or ops group chat id in spy-numbers.env" {
		t.Errorf("without Telegram: %s", s)
	}

	// The grouping merges OP8 into OP7: its mark goes along.
	b.exec(`UPDATE spy.account_operator SET operator_id = 7 WHERE account_id = 501`)
	b.exec(`DELETE FROM spy.operator WHERE id = 8`)
	if got := b.text(`SELECT spy.carry_operator_marks()::text`); got != "1" {
		t.Errorf("carried %s marks", got)
	}
	if got := b.text(`SELECT format('%s %s %s %s', operator_id, hidden::text, watched::text, nickname) FROM spy.operator_mark`); got != "7 true true Memo" {
		t.Errorf("after the merge: %s", got)
	}
	if got := b.text(`SELECT accounts::text FROM spy.operator_mark WHERE operator_id = 7`); got != "{500,501}" {
		t.Errorf("accounts: %s", got)
	}

	// Unmarked: the mark goes and the grouping names it again.
	b.exec(`SELECT spy_api.mark_operator_v1(7, false, false, '')`)
	if got := b.text(`SELECT count(*)::text FROM spy.operator_mark`); got != "0" {
		t.Errorf("marks left: %s", got)
	}
	if got := b.text(`SELECT name || '|' || name_is_manual FROM spy.operator WHERE id = 7`); got != "ClickBank memo · OP7|false" {
		t.Errorf("name after the nickname went: %s", got)
	}
}

// sentMessages stands in for the Telegram client and keeps what it was sent.
type sentMessages struct {
	mu    sync.Mutex
	texts []string
}

func (s *sentMessages) Send(_ context.Context, html string, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, html)
	return nil
}

func TestNoticeTextEscapesAndLinks(t *testing.T) {
	x := notice{operator: 9, title: "A&B <x> está subindo", body: "2 > 1"}
	if got, want := noticeText(x, ""), "👁 <b>A&amp;B &lt;x&gt; está subindo</b>\n2 &gt; 1"; got != want {
		t.Errorf("no base: %q, want %q", got, want)
	}
	if got := noticeText(x, "https://h"); !strings.HasSuffix(got, "\n<a href=\"https://h/spy/operators/9\">Abrir no Spy</a>") {
		t.Errorf("with base: %q", got)
	}
}
