package numbers

import (
	"strings"
	"testing"
	"time"
)

// walk adds one page of a walk as Tracks publishes it.
func (b *bench) walk(id, step, creative, account int, at time.Time, host, version string, status int,
	platform, seller string) {
	b.t.Helper()
	b.exec(`INSERT INTO tracks_api.walk_page_v1 (walk_id, at, ad_id, creative_id, account_id, step, url, final_url, host,
			status, checkout_platform, seller_account, version_hash)
		VALUES ($1, $2, $3, $3, NULLIF($4, 0), $5, 'https://link/', 'https://' || $6 || '/p', $6, $7,
			NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, '')::uuid)`,
		id, at, creative, account, step, host, status, platform, seller, version)
}

// version adds a page version with pixels (JSON), emails and companies.
func (b *bench) version(hash, title, pixels string, emails, companies []string) {
	b.t.Helper()
	b.exec(`INSERT INTO tracks_api.page_version_v1 (hash, title, pixels, emails, companies, text)
		VALUES ($1, $2, $3::jsonb, COALESCE($4, '{}'::text[]), COALESCE($5, '{}'::text[]), 'blood pressure support formula')`, hash, title, pixels, emails, companies)
}

const (
	v1 = "00000000-0000-0000-0000-000000000001"
	v2 = "00000000-0000-0000-0000-000000000002"
	v3 = "00000000-0000-0000-0000-000000000003"
	v4 = "00000000-0000-0000-0000-000000000004"
	v5 = "00000000-0000-0000-0000-000000000005"
)

func TestPagesAndOperators(t *testing.T) {
	b := newBench(t)
	b.exec(`INSERT INTO spy.operator (id, name, display_name) VALUES (8, 'old · OP8', 'old'), (20, 'unseen · OP20', 'unseen')`)
	week := now.AddDate(0, 0, -7)
	b.exec(`INSERT INTO tracks_api.account_v1 (id, external_id, first_seen_at, last_seen_at) VALUES
		(1, 'acmehealth-sc', $1, $2), (2, 'acmehealth2-sc', $1 + interval '1 day', $2), (3, 'wideguy-sc', $1, $2),
		(4, 'taboolaaccount-joe45gmailcom', $1, $2), (5, 'brandx-joe45gmailcom2-sc', $1, $2), (6, 'solo-sc', $1, $2),
		(9, 'nobody-sc', $1, $2)`, week, now)
	// Today's operators, as import-old copies them: 1 and 2 in OP7, 6 in OP8,
	// 9 (on no landing page) in OP20.
	b.exec(`INSERT INTO spy.account_operator VALUES (1, 7), (2, 7), (6, 8), (9, 20)`)

	fb := `{"facebook": ["1234567890123456"], "google": ["AW-123456789"], "redtrack": []}`
	b.version(v1, "Acme BP", fb, []string{"support@acmehealth.com"}, []string{"Acme Health LLC"})
	b.version(v2, "Acme BP 2", `{"facebook": ["1234567890123456"]}`, nil, nil)
	b.version(v3, "Other one", `{}`, nil, nil)
	b.version(v4, "Other two", `{}`, []string{"help@gmail.com"}, nil)
	b.version(v5, "Joe's shop", `{}`, []string{"help@gmail.com"}, nil)

	at := now.Add(-2 * time.Hour)
	b.walk(1, 0, 10, 1, at, "www.acme-a.com", v1, 200, "", "")
	b.walk(1, 1, 10, 1, at, "pay.clickbank.net", "", 200, "ClickBank", "slimpro")
	b.walk(2, 0, 11, 6, at, "shop.acme-b.co.uk", v2, 200, "", "")
	b.walk(3, 0, 12, 3, at, "acme-a.com", v1, 200, "", "")
	b.walk(4, 0, 13, 3, at, "other1.com", v3, 200, "", "")
	b.walk(5, 0, 14, 3, at, "other2.com", v4, 200, "", "")
	b.walk(6, 0, 15, 4, at, "joe.myshopify.com", v5, 200, "", "")
	b.walk(7, 0, 16, 2, at, "hop.clickbank.net", v3, 200, "ClickBank", "slimpro")
	b.walk(8, 0, 17, 1, at, "down.example", "", 503, "", "")
	// Creative 10 again, later, on another page.
	b.walk(9, 0, 10, 1, at.Add(time.Hour), "acme-a.com", v2, 200, "", "")

	if n, err := b.r.Pages(b.ctx); err != nil || n != 10 {
		t.Fatalf("pages: %d, %v", n, err)
	}
	if n, err := b.r.Pages(b.ctx); err != nil || n != 0 {
		t.Fatalf("pages again: %d, %v; want nothing new", n, err)
	}
	// Reading a range again (after a Tracks replay) changes nothing.
	if got := b.text(`SELECT spy.refresh_pages($1, $2)::text`, now, now.AddDate(0, 0, -1)); got != "10" {
		t.Fatalf("pages read again: %s", got)
	}
	if got := b.text(`SELECT string_agg(domain, ' ' ORDER BY domain) FROM spy.site`); got != "acme-a.com acme-b.co.uk joe.myshopify.com other1.com other2.com" {
		t.Errorf("sites: %s", got)
	}
	if got := b.text(`SELECT string_agg(format('%s:%s:%s', kind, value, strong::text), ' ' ORDER BY kind, value) FROM spy.clue`); got !=
		"google_ads:AW-123456789:true legal_text:Acme Health LLC:false meta_pixel:1234567890123456:true support_email:help@gmail.com:false support_email:support@acmehealth.com:true" {
		t.Errorf("clues: %s", got)
	}
	if got := b.text(`SELECT string_agg(format('%s %s', s.domain, se.account), ' ') FROM spy.site_seller x
		JOIN spy.site s ON s.id = x.site_id JOIN spy.seller se ON se.id = x.seller_id`); got != "acme-a.com slimpro" {
		t.Errorf("sellers: %s (a platform page is no one's site)", got)
	}
	if got := b.text(`SELECT format('%s %s', p.version_hash, s.domain) FROM spy.creative_page p JOIN spy.site s ON s.id = p.site_id
		WHERE creative_id = 10`); got != v2+" acme-a.com" {
		t.Errorf("creative 10's page: %s", got)
	}
	if got := b.text(`SELECT count(*)::text FROM spy.creative_page WHERE creative_id = 16 AND site_id IS NULL`); got != "1" {
		t.Errorf("a page on a platform still counts for its creative: %s", got)
	}

	// Shadow: a proposal only.
	if n, err := b.r.Operators(b.ctx); err != nil || n != 5 {
		t.Fatalf("operators: %d groups, %v", n, err)
	}
	groups := `SELECT string_agg(format('%s|%s|%s|%s|%s', COALESCE(g.operator_id::text, '-'), g.display_name, g.kind,
			COALESCE(g.seller, '-'),
			(SELECT string_agg(COALESCE(s.domain, 'account:' || m.member_id), ',' ORDER BY m.member, s.domain, m.member_id)
			 FROM spy.grouping_member m LEFT JOIN spy.site s ON m.member = 'site' AND s.id = m.member_id
			 WHERE m.grp = g.grp)), ' / ' ORDER BY g.display_name)
		FROM spy.grouping_group g`
	want := "7|acmehealth|affiliate|ClickBank slimpro|account:1,account:2,account:6,acme-a.com,acme-b.co.uk" +
		" / -|joe45gmailcom|direct|-|account:4,account:5,joe.myshopify.com" +
		" / -|other1.com|direct|-|other1.com" +
		" / -|other2.com|direct|-|other2.com" +
		" / -|wideguy|arbitrage|-|account:3"
	if got := b.text(groups); got != want {
		t.Errorf("groups:\n got %s\nwant %s", got, want)
	}
	if got := b.text(`SELECT format('%s %s %s %s %s', applied::text, accounts_same, accounts_moved, accounts_new, accounts_unseen)
		FROM spy.grouping_run ORDER BY id DESC LIMIT 1`); got != "false 2 1 3 1" {
		t.Errorf("comparison: %s", got)
	}
	if got := b.text(`SELECT string_agg(account_id || '>' || operator_id, ' ' ORDER BY account_id) FROM spy.account_operator`); got != "1>7 2>7 6>8 9>20" {
		t.Errorf("shadow changed the operators in use: %s", got)
	}

	// The check lists the proposed groups that differ from today's operators.
	var shadow strings.Builder
	if err := Check(b.ctx, b.db, &shadow, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"acmehealth (OP7)  OP7 2, OP8 1", "(proposed only)", "of those, moved or split out             1"} {
		if !strings.Contains(shadow.String(), want) {
			t.Errorf("shadow check has no %q:\n%s", want, shadow.String())
		}
	}

	// Applied, with a hand fix keeping other2.com apart.
	b.exec(`UPDATE spy.setting SET text_value = 'grouping' WHERE name = 'operators_from'`)
	b.exec(`INSERT INTO spy.grouping_fix (site_id, action, note) SELECT id, 'split', 'not theirs' FROM spy.site WHERE domain = 'other2.com'`)
	if n, err := b.r.Operators(b.ctx); err != nil || n != 4 {
		t.Fatalf("operators applied: %d groups, %v", n, err)
	}
	if got := b.text(`SELECT name FROM spy.operator WHERE id = 7`); got != "acmehealth · ClickBank slimpro · OP7" {
		t.Errorf("OP7: %s", got)
	}
	if got := b.text(`SELECT count(*)::text FROM spy.operator WHERE id = 8`); got != "0" {
		t.Errorf("OP8 holds nothing now and should be gone")
	}
	if got := b.text(`SELECT count(*)::text FROM spy.operator WHERE id = 20`); got != "1" {
		t.Errorf("OP20's account is on no landing page yet and keeps it")
	}
	if got := b.text(`SELECT string_agg(ao.account_id || '>' || o.display_name, ' ' ORDER BY ao.account_id)
		FROM spy.account_operator ao JOIN spy.operator o ON o.id = ao.operator_id`); got !=
		"1>acmehealth 2>acmehealth 3>wideguy 4>joe45gmailcom 5>joe45gmailcom 6>acmehealth 9>unseen" {
		t.Errorf("accounts: %s", got)
	}
	if got := b.text(`SELECT format('%s %s', (operator_id IS NULL)::text, group_reason) FROM spy.site WHERE domain = 'other2.com'`); got != "true split by hand: not theirs" {
		t.Errorf("split site: %s", got)
	}
	if got := b.text(`SELECT group_reason FROM spy.site WHERE domain = 'acme-b.co.uk'`); got != "buys ads only for this operator's sites" {
		t.Errorf("reason: %s", got)
	}
	// Again: nothing moves, the ids stay.
	ids := `SELECT string_agg(account_id || '>' || operator_id, ' ' ORDER BY account_id) FROM spy.account_operator`
	before := b.text(ids)
	if _, err := b.r.Operators(b.ctx); err != nil {
		t.Fatal(err)
	}
	if after := b.text(ids); after != before {
		t.Errorf("ids moved on a second run: %s, then %s", before, after)
	}
	if got := b.text(`SELECT format('%s %s %s %s', accounts_same, accounts_moved, accounts_new, accounts_unseen) FROM spy.grouping_run ORDER BY id DESC LIMIT 1`); got != "6 0 0 1" {
		t.Errorf("second run comparison: %s", got)
	}

	var out strings.Builder
	if err := Check(b.ctx, b.db, &out, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"walks with a page                 9", "ClickBank 2", "accounts with an operator now", "not on a landing page yet (keep theirs)",
		"operators_from"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("check has no %q:\n%s", want, out.String())
		}
	}
}

// An agency whose other clients the walker has not reached yet: its accounts
// belong to 3 operators today, so the root does not join them.
func TestAgencyRootFromTodaysOperators(t *testing.T) {
	b := newBench(t)
	week := now.AddDate(0, 0, -7)
	b.exec(`INSERT INTO spy.operator (id, name) VALUES (31, 'a · OP31'), (32, 'b · OP32'), (33, 'c · OP33'), (34, 'd · OP34')`)
	b.exec(`INSERT INTO tracks_api.account_v1 (id, external_id, first_seen_at, last_seen_at) VALUES
		(1, 'bigagency-c1', $1, $2), (2, 'bigagency-c2', $1, $2), (3, 'bigagency-c3', $1, $2),
		(4, 'onebrand-us1', $1, $2), (5, 'onebrand-us2', $1, $2)`, week, now)
	b.exec(`INSERT INTO spy.account_operator VALUES (1, 31), (2, 32), (3, 33), (4, 34), (5, 34)`)
	b.version(v1, "Client one", `{}`, nil, nil)
	b.version(v2, "Brand", `{}`, nil, nil)
	at := now.Add(-2 * time.Hour)
	b.walk(1, 0, 10, 1, at, "client1.com", v1, 200, "", "")
	b.walk(2, 0, 11, 4, at, "onebrand.com", v2, 200, "", "")
	if _, err := b.r.Pages(b.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.r.Operators(b.ctx); err != nil {
		t.Fatal(err)
	}
	groups := `SELECT string_agg(x, ' / ' ORDER BY x) FROM (
		SELECT string_agg(COALESCE(s.domain, 'account:' || m.member_id), ',' ORDER BY m.member, s.domain, m.member_id) x
		FROM spy.grouping_member m LEFT JOIN spy.site s ON m.member = 'site' AND s.id = m.member_id
		WHERE m.grp IS NOT NULL GROUP BY m.grp) g`
	if got := b.text(groups); got != "account:1,client1.com / account:4,account:5,onebrand.com" {
		t.Errorf("groups: %s; the agency's unwalked accounts should stay apart, one brand's join", got)
	}
	if got := b.text(`SELECT string_agg(name_root || ' ' || operator_count, ', ') FROM spy.agency`); got != "bigagency 3" {
		t.Errorf("agencies: %s", got)
	}
}
