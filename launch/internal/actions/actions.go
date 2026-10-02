// Package actions is what Launch does on an ad network, each step recorded
// in History: a new group, a new desktop and mobile pair with its ads, a
// copy, a move to another group, a pause, a change of bid or caps. It is the
// one place Launch's writes go through, so Intel's and Desk's requests
// (later, through launch_api) get the same checks and the same History.
//
// New groups, campaigns and ads go up paused, or running when the network
// client is set to (TABOOLA_CREATE_ACTIVE, the owner's word of 2026-10-02):
// a person asking for a new campaign here is the go. Copies and moves are
// always paused, and nothing here turns an existing campaign on. A move's
// originals are paused when a person starts the copies (Watch), so the two
// never spend at once.
package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Raposa-Industries/adhunters/launch/internal/images"
	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
)

// Launch holds the networks, Launch's records and its pictures.
type Launch struct {
	nets map[string]network.Network
	st   *store.Store
	img  *images.Store
	log  *slog.Logger
	// Say turns an error into one pt-BR line for History and the page.
	Say func(error) string
}

// New returns Launch over the given networks.
func New(st *store.Store, img *images.Store, log *slog.Logger, say func(error) string, nets ...network.Network) *Launch {
	l := &Launch{nets: map[string]network.Network{}, st: st, img: img, log: log, Say: say}
	for _, n := range nets {
		l.nets[n.Name()] = n
	}
	return l
}

// Net returns the network a path names ("taboola").
func (l *Launch) Net(name string) (network.Network, error) {
	n, ok := l.nets[name]
	if !ok {
		return nil, &network.Refused{Message: "rede " + name + " desconhecida"}
	}
	return n, nil
}

// Networks lists the networks Launch knows, in name order.
func (l *Launch) Networks() []network.Network {
	var out []network.Network
	for _, name := range []string{"taboola", "newsbreak"} {
		if n, ok := l.nets[name]; ok {
			out = append(out, n)
		}
	}
	return out
}

// Store is Launch's records, for reading.
func (l *Launch) Store() *store.Store { return l.st }

// Who says who did something and who asked for it.
type Who struct {
	Person  string // the signed-in person
	AskedBy string // "" when the person asked; "intel:311", "desk:…"
}

func (w Who) asked() string {
	if w.AskedBy != "" {
		return w.AskedBy
	}
	return w.Person
}

func raw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func (l *Launch) record(ctx context.Context, c store.Change) int64 {
	id, err := l.st.Record(ctx, c)
	if err != nil {
		// The change happened on the network; History missing a row is
		// logged loudly rather than hiding what was done.
		l.log.Error("history not recorded", "kind", c.Kind, "campaign", c.CampaignID, "summary", c.Summary, "err", err)
	}
	return id
}

// noteItems keeps the ads a create or copy made, for launch_api.item_v1.
func (l *Launch) noteItems(ctx context.Context, net, account string, m network.Made) {
	items := map[string]string{}
	for _, a := range m.Ads {
		if a.ID != "" {
			items[a.ID] = a.AdID
		}
	}
	if err := l.st.AddItems(ctx, net, account, m.Campaign.ID, items); err != nil {
		l.log.Error("ads made not noted", "campaign", m.Campaign.ID, "ads", len(items), "err", err)
	}
}

// NewGroup makes a group. Without a name it gets the account's next
// number (01, 02…).
func (l *Launch) NewGroup(ctx context.Context, who Who, net, account string, g network.NewGroup) (network.Group, error) {
	n, err := l.Net(net)
	if err != nil {
		return network.Group{}, err
	}
	if strings.TrimSpace(g.Name) == "" {
		groups, err := n.Groups(ctx, account)
		if err != nil {
			return network.Group{}, err
		}
		g.Name = NextGroupName(groups)
	}
	made, err := n.CreateGroup(ctx, account, g)
	if err != nil {
		return network.Group{}, err
	}
	l.record(ctx, store.Change{
		Who: who.Person, AskedBy: who.asked(), Network: net, Account: account, GroupID: made.ID, Kind: "new_group",
		Summary: "Criou o grupo " + made.Name + ", " + groupBudget(made),
		After:   raw(made), Result: "done",
	})
	return made, nil
}

// PairRequest is a new desktop and mobile pair.
type PairRequest struct {
	Network string `json:"network"`
	Account string `json:"account"`
	// Name is the pair's; Launch adds " · Desktop" and " · Mobile". Empty:
	// the team's names (CampaignName), numbered at send time.
	Name string `json:"name"`
	// DesktopName and MobileName, when set, name that campaign only; the
	// other keeps Name's or the team's.
	DesktopName string `json:"desktop_name,omitempty"`
	MobileName  string `json:"mobile_name,omitempty"`
	// Devices is desktop, mobile or both (the default): one campaign each.
	Devices string `json:"devices"`
	// GroupID is where both go; NewGroup makes a group for them first.
	GroupID  string            `json:"group_id"`
	NewGroup *network.NewGroup `json:"new_group,omitempty"`
	Settings network.Settings  `json:"settings"`
	// Desktop and Mobile, when set, replace Settings for that device only.
	Desktop  *network.Settings `json:"desktop,omitempty"`
	Mobile   *network.Settings `json:"mobile,omitempty"`
	Ads      []network.NewAd   `json:"ads"`
	PresetID *int64            `json:"preset_id,omitempty"`
	DraftID  int64             `json:"draft_id,omitempty"`
}

// Names are the campaigns' names when the request names them.
func (r PairRequest) Names() (desktop, mobile string) {
	n := strings.TrimSpace(r.Name)
	return n + " · Desktop", n + " · Mobile"
}

// NextNames are the names a new group and campaign get in an account now.
type NextNames struct {
	Group         string `json:"group"`
	Campaign      int    `json:"campaign"`
	AccountNumber string `json:"account_number"`
	Desktop       string `json:"desktop"`
	Mobile        string `json:"mobile"`
}

// Next reads the account's groups and campaigns for the next names.
func (l *Launch) Next(ctx context.Context, net, account string) (NextNames, error) {
	n, err := l.Net(net)
	if err != nil {
		return NextNames{}, err
	}
	groups, err := n.Groups(ctx, account)
	if err != nil {
		return NextNames{}, err
	}
	camps, err := n.Campaigns(ctx, account)
	if err != nil {
		return NextNames{}, err
	}
	accts, _ := n.Accounts(ctx)
	out := NextNames{Group: NextGroupName(groups), Campaign: NextCampaignNumber(camps), AccountNumber: AccountNumber(account, accts)}
	out.Desktop = CampaignName(out.Campaign, out.AccountNumber, network.Desktop)
	out.Mobile = CampaignName(out.Campaign, out.AccountNumber, network.Mobile)
	return out, nil
}

// Check refuses a request before anything is sent.
func (r PairRequest) Check() error {
	switch {
	case strings.TrimSpace(r.Account) == "":
		return &network.Refused{Message: "escolha a conta"}
	case r.GroupID == "" && r.NewGroup == nil:
		return &network.Refused{Message: "escolha o grupo ou crie um novo: o Launch sempre põe as campanhas num grupo"}
	}
	for i, a := range r.Ads {
		if a.Image == "" {
			return &network.Refused{Message: fmt.Sprintf("o anúncio %d não tem imagem", i+1)}
		}
	}
	return nil
}

// Step is one line of a send's progress.
type Step struct {
	Label  string `json:"label"`
	State  string `json:"state"` // wait, run, ok, fail
	Detail string `json:"detail,omitempty"`
}

// PairResult is what a new pair made.
type PairResult struct {
	PairID   int64          `json:"pair_id"`
	Group    *network.Group `json:"group,omitempty"`
	GroupID  string         `json:"group_id"`
	Desktop  *network.Made  `json:"desktop,omitempty"`
	Mobile   *network.Made  `json:"mobile,omitempty"`
	Problems []string       `json:"problems"`
	Result   string         `json:"result"` // done, partial, failed
}

// NewPair makes the group (when asked), then the desktop and the mobile
// campaign with the same ads, all paused, and records the pair and History.
// A campaign that fails does not stop the other; the result says what was
// made. progress, when set, hears each step as it changes.
func (l *Launch) NewPair(ctx context.Context, who Who, r PairRequest, progress func([]Step)) (PairResult, error) {
	res := PairResult{Problems: []string{}}
	if err := r.Check(); err != nil {
		return res, err
	}
	n, err := l.Net(r.Network)
	if err != nil {
		return res, err
	}
	devices, err := Devices(r.Devices)
	if err != nil {
		return res, err
	}
	var next NextNames
	teamNames := strings.TrimSpace(r.Name) == ""
	if teamNames || (r.NewGroup != nil && strings.TrimSpace(r.NewGroup.Name) == "") {
		if next, err = l.Next(ctx, r.Network, r.Account); err != nil {
			return res, err
		}
	}
	if r.NewGroup != nil && strings.TrimSpace(r.NewGroup.Name) == "" {
		g := *r.NewGroup
		g.Name = next.Group
		r.NewGroup = &g
	}
	dName, mName := r.Names()
	if teamNames {
		dName, mName = next.Desktop, next.Mobile
		r.Name = fmt.Sprintf("CMP%02d-%s", next.Campaign, next.AccountNumber)
	}
	if n := strings.TrimSpace(r.DesktopName); n != "" {
		dName = n
	}
	if n := strings.TrimSpace(r.MobileName); n != "" {
		mName = n
	}
	steps := []Step{{Label: "Grupo"}}
	for _, d := range devices {
		if d == network.Desktop {
			steps = append(steps, Step{Label: dName})
		} else {
			steps = append(steps, Step{Label: mName})
		}
	}
	tell := func(i int, state, detail string) {
		steps[i].State, steps[i].Detail = state, detail
		if progress != nil {
			progress(append([]Step(nil), steps...))
		}
	}
	for i := range steps {
		steps[i].State = "wait"
	}

	// The group.
	res.GroupID = r.GroupID
	if r.NewGroup != nil {
		tell(0, "run", "criando "+r.NewGroup.Name)
		g, err := l.NewGroup(ctx, who, r.Network, r.Account, *r.NewGroup)
		if err != nil {
			tell(0, "fail", l.Say(err))
			res.Result = "failed"
			res.Problems = append(res.Problems, "grupo: "+l.Say(err))
			return res, nil
		}
		res.Group, res.GroupID = &g, g.ID
		tell(0, "ok", "criado · "+g.ID)
	} else {
		tell(0, "ok", "já existe · "+r.GroupID)
	}

	pair := store.Pair{Network: r.Network, Account: r.Account, GroupID: res.GroupID, Name: strings.TrimSpace(r.Name), PresetID: r.PresetID, MadeBy: who.Person}
	up := &network.Uploads{Read: l.img.Get}
	made := 0
	state := "pausada" // the campaigns', as the network made them
	type side struct {
		dev  network.Device
		name string
		set  *network.Settings
	}
	var sides []side
	for _, d := range devices {
		if d == network.Desktop {
			sides = append(sides, side{d, dName, r.Desktop})
		} else {
			sides = append(sides, side{d, mName, r.Mobile})
		}
	}
	for i, side := range sides {
		set := r.Settings
		if side.set != nil {
			set = *side.set
		}
		if len(r.Ads) > 0 {
			tell(i+1, "run", "criando a campanha e "+ads(len(r.Ads)))
		} else {
			tell(i+1, "run", "criando a campanha, sem anúncios")
		}
		m, err := n.CreateCampaign(ctx, r.Account, network.NewCampaign{Name: side.name, GroupID: res.GroupID, Device: side.dev, Settings: set, Ads: r.Ads}, up)
		if m.Campaign.ID != "" {
			made++
			if m.Campaign.Active {
				state = "ativa"
			}
			l.noteItems(ctx, r.Network, r.Account, m)
			mm := m
			if side.dev == network.Desktop {
				res.Desktop, pair.DesktopID = &mm, m.Campaign.ID
			} else {
				res.Mobile, pair.MobileID = &mm, m.Campaign.ID
			}
		}
		switch {
		case err != nil && m.Campaign.ID == "":
			tell(i+1, "fail", l.Say(err))
			res.Problems = append(res.Problems, side.name+": "+l.Say(err))
		case err != nil:
			tell(i+1, "fail", fmt.Sprintf("campanha %s criada, %s; %d de %s: %s", m.Campaign.ID, onOff(m.Campaign.Active, "ativa", "pausada"), len(m.Ads), ads(len(r.Ads)), l.Say(err)))
			res.Problems = append(res.Problems, side.name+": "+l.Say(err))
		case len(r.Ads) == 0:
			tell(i+1, "ok", fmt.Sprintf("criada, %s · %s · sem anúncios", onOff(m.Campaign.Active, "ativa", "pausada"), m.Campaign.ID))
		default:
			tell(i+1, "ok", fmt.Sprintf("criada, %s · %s · %s, %s", onOff(m.Campaign.Active, "ativa", "pausada"), m.Campaign.ID, ads(len(m.Ads)), adsState(m.Ads)))
		}
	}

	switch {
	case made == len(sides) && len(res.Problems) == 0:
		res.Result = "done"
	case made == 0:
		res.Result = "failed"
	default:
		res.Result = "partial"
	}
	if made > 0 && len(sides) == 2 {
		if res.PairID, err = l.st.AddPair(ctx, pair); err != nil {
			l.log.Error("pair not recorded", "desktop", pair.DesktopID, "mobile", pair.MobileID, "err", err)
			res.Problems = append(res.Problems, "o par foi criado mas não foi anotado aqui; ele aparece como duas campanhas soltas")
		}
	}
	each, with := ads(len(r.Ads))+" em cada", "com "+ads(len(r.Ads))
	if len(r.Ads) == 0 {
		each, with = "sem anúncios", "sem anúncios"
	}
	summary := fmt.Sprintf("Criou %s e %s, %ss: %d de 2 campanhas, %s", sides[0].name, sides[len(sides)-1].name, state, made, each)
	if len(sides) == 1 {
		summary = fmt.Sprintf("Criou a campanha %s, %s, %s", sides[0].name, state, with)
		if made == 0 {
			summary = "Tentou criar a campanha " + sides[0].name
		}
	}
	first := pair.DesktopID
	if first == "" {
		first = pair.MobileID
	}
	l.record(ctx, store.Change{
		Who: who.Person, AskedBy: who.asked(), Network: r.Network, Account: r.Account, GroupID: res.GroupID,
		CampaignID: first, Kind: "new_pair",
		Summary: summary,
		After:   raw(res), Result: res.Result, Problems: res.Problems,
	})
	if pair.MobileID != "" && pair.DesktopID != "" {
		l.record(ctx, store.Change{
			Who: who.Person, AskedBy: who.asked(), Network: r.Network, Account: r.Account, GroupID: res.GroupID,
			CampaignID: pair.MobileID, Kind: "new_pair",
			Summary: fmt.Sprintf("Criou %s, %s, junto com %s", sides[1].name, state, sides[0].name), Result: res.Result,
		})
	}
	if res.Result == "done" && r.DraftID != 0 {
		_ = l.st.DeleteDraft(ctx, r.DraftID)
	}
	return res, nil
}

// Done is one campaign an action went through, and how.
type Done struct {
	Campaign string            `json:"campaign"`
	Copy     *network.Campaign `json:"copy,omitempty"`
	Ads      int               `json:"ads,omitempty"`
	Ad       string            `json:"ad,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// MoveRequest moves campaigns to another group: a paused copy there with
// its ads, and the originals paused when the copies start (or now, or never).
type MoveRequest struct {
	Network   string   `json:"network"`
	Account   string   `json:"account"`
	Campaigns []string `json:"campaigns"`
	ToGroup   string   `json:"to_group"`
	Originals string   `json:"originals"` // when_started (default), now, leave
}

// Move copies each campaign into ToGroup. Taboola cannot change a
// campaign's group, so the copy has a new id; History keeps both, and the
// original keeps its own history.
func (l *Launch) Move(ctx context.Context, who Who, r MoveRequest) ([]Done, error) {
	if r.Originals == "" {
		r.Originals = "when_started"
	}
	if r.Originals != "when_started" && r.Originals != "now" && r.Originals != "leave" {
		return nil, &network.Refused{Message: "escolha o que fazer com as originais"}
	}
	if strings.TrimSpace(r.ToGroup) == "" {
		return nil, &network.Refused{Message: "escolha o grupo de destino"}
	}
	// History names the group; its id is kept in the change's group_id.
	toName := r.ToGroup
	if n, err := l.Net(r.Network); err == nil {
		if groups, err := n.Groups(ctx, r.Account); err == nil {
			for _, g := range groups {
				if g.ID == r.ToGroup && g.Name != "" {
					toName = g.Name
				}
			}
		}
	}
	return l.copyEach(ctx, who, r.Network, r.Account, r.Campaigns, func(c network.Campaign) (network.CopyTo, error) {
		if c.GroupID == r.ToGroup {
			return network.CopyTo{}, &network.Refused{Message: "a campanha já está nesse grupo"}
		}
		return network.CopyTo{Name: c.Name, GroupID: r.ToGroup}, nil
	}, func(c network.Campaign, m network.Made, n network.Network) store.Change {
		ch := store.Change{
			Kind: "move", GroupID: c.GroupID,
			Summary: fmt.Sprintf("Movendo %s para o grupo %s: cópia %s feita, pausada, com %s.", c.Name, toName, m.Campaign.ID, ads(len(m.Ads))),
			Before:  raw(c), After: raw(m.Campaign), Result: "done",
		}
		switch r.Originals {
		case "when_started":
			ch.Summary += " A original pausa quando a cópia começar."
			ch.Result = "waiting"
		case "now":
			if err := n.Pause(ctx, r.Account, c.ID); err != nil {
				ch.Problems = append(ch.Problems, "a original não pausou: "+l.Say(err))
				ch.Result = "partial"
			} else {
				ch.Summary += " A original foi pausada."
			}
		case "leave":
			ch.Summary += " A original ficou como estava."
		}
		return ch
	}, func(changeID int64, c network.Campaign, m network.Made) {
		if _, err := l.st.AddMove(ctx, store.Move{ChangeID: changeID, Network: r.Network, Account: r.Account, FromCampaign: c.ID, ToCampaign: m.Campaign.ID, ToGroup: r.ToGroup, Originals: r.Originals}); err != nil {
			l.log.Error("move not recorded", "from", c.ID, "to", m.Campaign.ID, "err", err)
		}
	})
}

// Duplicate makes a paused copy of each campaign, with its ads, in its own
// group.
func (l *Launch) Duplicate(ctx context.Context, who Who, net, account string, campaigns []string) ([]Done, error) {
	return l.copyEach(ctx, who, net, account, campaigns, func(c network.Campaign) (network.CopyTo, error) {
		return network.CopyTo{Name: c.Name + " (cópia)"}, nil
	}, func(c network.Campaign, m network.Made, _ network.Network) store.Change {
		return store.Change{
			Kind: "copy", GroupID: c.GroupID,
			Summary: fmt.Sprintf("Copiou %s: %s, pausada, com %s", c.Name, m.Campaign.ID, ads(len(m.Ads))),
			Before:  raw(c), After: raw(m.Campaign), Result: "done",
		}
	}, nil)
}

// copyEach copies campaigns one by one; one that fails does not stop the
// rest. When both campaigns of a pair are copied, the copies are a pair too.
func (l *Launch) copyEach(ctx context.Context, who Who, net, account string, ids []string,
	to func(network.Campaign) (network.CopyTo, error),
	describe func(network.Campaign, network.Made, network.Network) store.Change,
	after func(int64, network.Campaign, network.Made),
) ([]Done, error) {
	n, err := l.Net(net)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, &network.Refused{Message: "escolha ao menos uma campanha"}
	}
	pairs, _ := l.st.Pairs(ctx, net, account)
	copied := map[string]network.Made{}
	var out []Done
	for _, id := range ids {
		d := Done{Campaign: id}
		c, err := n.Campaign(ctx, account, id)
		if err != nil {
			d.Error = l.Say(err)
			out = append(out, d)
			continue
		}
		dest, err := to(c)
		if err != nil {
			d.Error = l.Say(err)
			out = append(out, d)
			continue
		}
		m, err := n.Copy(ctx, account, id, dest)
		if m.Campaign.ID == "" {
			d.Error = l.Say(err)
			out = append(out, d)
			continue
		}
		copied[id] = m
		l.noteItems(ctx, net, account, m)
		d.Copy, d.Ads = &m.Campaign, len(m.Ads)
		ch := describe(c, m, n)
		ch.Who, ch.AskedBy, ch.Network, ch.Account, ch.CampaignID = who.Person, who.asked(), net, account, id
		if err != nil {
			d.Error = "cópia feita, mas: " + l.Say(err)
			ch.Problems = append(ch.Problems, d.Error)
			ch.Result = "partial"
		}
		changeID := l.record(ctx, ch)
		if after != nil && changeID != 0 {
			after(changeID, c, m)
		}
		out = append(out, d)
	}
	for _, p := range pairs {
		dm, dok := copied[p.DesktopID]
		mm, mok := copied[p.MobileID]
		if !dok || !mok {
			continue
		}
		name := p.Name
		if dm.Campaign.GroupID == p.GroupID {
			name += " (cópia)"
		}
		if _, err := l.st.AddPair(ctx, store.Pair{Network: net, Account: account, GroupID: dm.Campaign.GroupID, Name: name,
			DesktopID: dm.Campaign.ID, MobileID: mm.Campaign.ID, PresetID: p.PresetID, MadeBy: who.Person}); err != nil {
			l.log.Error("copied pair not recorded", "err", err)
		}
	}
	return out, nil
}

// Pause pauses each campaign.
func (l *Launch) Pause(ctx context.Context, who Who, net, account string, ids []string) ([]Done, error) {
	n, err := l.Net(net)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, &network.Refused{Message: "escolha ao menos uma campanha"}
	}
	var out []Done
	for _, id := range ids {
		d := Done{Campaign: id}
		c, err := n.Campaign(ctx, account, id)
		if err == nil {
			err = n.Pause(ctx, account, id)
		}
		ch := store.Change{Who: who.Person, AskedBy: who.asked(), Network: net, Account: account, GroupID: c.GroupID, CampaignID: id,
			Kind: "pause", Summary: "Pausou " + orID(c.Name, id), Before: raw(map[string]any{"active": c.Active, "status": c.Status}),
			After: raw(map[string]any{"active": false}), Result: "done"}
		if err != nil {
			d.Error = l.Say(err)
			ch.Result, ch.Problems, ch.Summary = "failed", []string{d.Error}, "Tentou pausar "+orID(c.Name, id)
		}
		l.record(ctx, ch)
		out = append(out, d)
	}
	return out, nil
}

// AddAds makes the same new ads in each campaign (Realize's
// "assign creatives"). A picture is uploaded once for all of them.
func (l *Launch) AddAds(ctx context.Context, who Who, net, account string, campaigns []string, newAds []network.NewAd) ([]Done, error) {
	n, err := l.Net(net)
	if err != nil {
		return nil, err
	}
	if len(campaigns) == 0 {
		return nil, &network.Refused{Message: "escolha ao menos uma campanha"}
	}
	if len(newAds) == 0 {
		return nil, &network.Refused{Message: "escolha ao menos um anúncio"}
	}
	for i, a := range newAds {
		if a.Image == "" {
			return nil, &network.Refused{Message: fmt.Sprintf("o anúncio %d não tem imagem", i+1)}
		}
	}
	up := &network.Uploads{Read: l.img.Get}
	var out []Done
	for _, id := range campaigns {
		d := Done{Campaign: id}
		c, err := n.Campaign(ctx, account, id)
		var m network.Made
		if err == nil {
			m, err = n.AddAds(ctx, account, id, newAds, up)
		}
		d.Ads = len(m.Ads)
		if len(m.Ads) > 0 {
			m.Campaign.ID = id
			l.noteItems(ctx, net, account, m)
		}
		ch := store.Change{Who: who.Person, AskedBy: who.asked(), Network: net, Account: account, GroupID: c.GroupID, CampaignID: id,
			Kind: "change", Summary: fmt.Sprintf("Adicionou %s, %s, em %s", ads(len(m.Ads)), adsState(m.Ads), orID(c.Name, id)),
			After: raw(m.Ads), Result: "done"}
		if err != nil {
			d.Error = l.Say(err)
			ch.Problems = []string{d.Error}
			ch.Result = "partial"
			if len(m.Ads) == 0 {
				ch.Result, ch.Summary = "failed", "Tentou adicionar "+ads(len(newAds))+" em "+orID(c.Name, id)
			}
		}
		l.record(ctx, ch)
		out = append(out, d)
	}
	return out, nil
}

// PauseAds pauses some ads of one campaign, recorded as one pause in
// History.
func (l *Launch) PauseAds(ctx context.Context, who Who, net, account, campaign string, ids []string) ([]Done, error) {
	n, err := l.Net(net)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, &network.Refused{Message: "escolha ao menos um anúncio"}
	}
	c, err := n.Campaign(ctx, account, campaign)
	if err != nil {
		return nil, err
	}
	var out []Done
	var paused, problems []string
	for _, id := range ids {
		d := Done{Campaign: campaign, Ad: id}
		if err := n.PauseAd(ctx, account, campaign, id); err != nil {
			d.Error = l.Say(err)
			problems = append(problems, id+": "+d.Error)
		} else {
			paused = append(paused, id)
		}
		out = append(out, d)
	}
	ch := store.Change{Who: who.Person, AskedBy: who.asked(), Network: net, Account: account, GroupID: c.GroupID, CampaignID: campaign,
		Kind: "pause", Summary: "Pausou " + ads(len(paused)) + " de " + orID(c.Name, campaign),
		Before: raw(map[string]any{"ads": ids}), After: raw(map[string]any{"paused": paused}), Result: "done", Problems: problems}
	switch {
	case len(paused) == 0:
		ch.Result, ch.Summary = "failed", "Tentou pausar "+ads(len(ids))+" de "+orID(c.Name, campaign)
	case len(problems) > 0:
		ch.Result = "partial"
	}
	l.record(ctx, ch)
	return out, nil
}

// Change changes bid, caps or name on each campaign, recording before and
// after.
func (l *Launch) Change(ctx context.Context, who Who, net, account string, ids []string, change network.Change) ([]Done, error) {
	n, err := l.Net(net)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, &network.Refused{Message: "escolha ao menos uma campanha"}
	}
	if len(ids) > 1 && change.Name != "" {
		return nil, &network.Refused{Message: "mude o nome de uma campanha por vez"}
	}
	var out []Done
	for _, id := range ids {
		d := Done{Campaign: id}
		c, err := n.Campaign(ctx, account, id)
		var got network.Campaign
		if err == nil {
			got, err = n.Change(ctx, account, id, change)
		}
		ch := store.Change{Who: who.Person, AskedBy: who.asked(), Network: net, Account: account, GroupID: c.GroupID, CampaignID: id,
			Kind: "change", Summary: "Mudou " + orID(c.Name, id) + ": " + describeChange(c, change),
			Before: raw(c.Settings), After: raw(got.Settings), Result: "done"}
		if err != nil {
			d.Error = l.Say(err)
			ch.Result, ch.Problems, ch.After = "failed", []string{d.Error}, raw(change)
		}
		l.record(ctx, ch)
		out = append(out, d)
	}
	return out, nil
}

func groupBudget(g network.Group) string {
	per := map[string]string{"MONTHLY": " por mês", "ENTIRE": " no total"}[g.BudgetModel]
	if per == "" || g.Budget == 0 {
		return "orçamento por campanha"
	}
	return fmt.Sprintf("orçamento US$ %.2f%s", g.Budget, per)
}

func describeChange(c network.Campaign, ch network.Change) string {
	var parts []string
	if ch.CPC > 0 {
		parts = append(parts, fmt.Sprintf("lance US$ %.2f → US$ %.2f", c.Settings.CPC, ch.CPC))
	}
	if ch.DailyCap > 0 {
		parts = append(parts, fmt.Sprintf("limite diário US$ %.2f → US$ %.2f", c.Settings.DailyCap, ch.DailyCap))
	}
	if ch.SpendingLimit > 0 {
		parts = append(parts, fmt.Sprintf("limite total US$ %.2f → US$ %.2f", c.Settings.SpendingLimit, ch.SpendingLimit))
	}
	if ch.Name != "" {
		parts = append(parts, fmt.Sprintf("nome %q → %q", c.Name, ch.Name))
	}
	return strings.Join(parts, ", ")
}

// ads is "1 anúncio" or "n anúncios".
func ads(n int) string {
	if n == 1 {
		return "1 anúncio"
	}
	return fmt.Sprintf("%d anúncios", n)
}

func orID(name, id string) string {
	if name == "" {
		return id
	}
	return name
}

// Watch goes through the moves whose originals wait for their copies to
// start: when a person has started a copy in the network's dashboard, the
// original is paused, so the two never spend at once. It returns how many
// originals it paused.
func (l *Launch) Watch(ctx context.Context) (int, error) {
	moves, err := l.st.Waiting(ctx)
	if err != nil {
		return 0, err
	}
	paused := 0
	var errs []error
	for _, m := range moves {
		n, err := l.Net(m.Network)
		if err != nil {
			continue
		}
		cp, err := n.Campaign(ctx, m.Account, m.ToCampaign)
		if err != nil {
			errs = append(errs, fmt.Errorf("move %d: copy %s: %w", m.ID, m.ToCampaign, err))
			continue
		}
		if !cp.Active {
			continue
		}
		if err := n.Pause(ctx, m.Account, m.FromCampaign); err != nil {
			errs = append(errs, fmt.Errorf("move %d: pause %s: %w", m.ID, m.FromCampaign, err))
			continue
		}
		ok, err := l.st.EndMove(ctx, m.ID, "done")
		if err != nil || !ok {
			continue
		}
		paused++
		_ = l.st.SetResult(ctx, m.ChangeID, "done")
		l.record(ctx, store.Change{Who: "Launch", AskedBy: "Launch", Network: m.Network, Account: m.Account, GroupID: m.ToGroup, CampaignID: m.FromCampaign,
			Kind: "move_done", Summary: fmt.Sprintf("A cópia %s começou; a original %s foi pausada", m.ToCampaign, m.FromCampaign), Result: "done"})
	}
	return paused, errors.Join(errs...)
}

// CancelMove stops waiting on a move: the copy stays, paused, and the
// original stays as it is.
func (l *Launch) CancelMove(ctx context.Context, who Who, id int64) error {
	moves, err := l.st.Waiting(ctx)
	if err != nil {
		return err
	}
	for _, m := range moves {
		if m.ID != id {
			continue
		}
		ok, err := l.st.EndMove(ctx, id, "cancelled")
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		_ = l.st.SetResult(ctx, m.ChangeID, "done")
		l.record(ctx, store.Change{Who: who.Person, AskedBy: who.asked(), Network: m.Network, Account: m.Account, CampaignID: m.FromCampaign, Kind: "move_done",
			Summary: fmt.Sprintf("Cancelou a espera da mudança: a original %s fica como está, a cópia %s fica pausada", m.FromCampaign, m.ToCampaign), Result: "done"})
		return nil
	}
	return &network.Refused{Message: "essa mudança não está mais esperando"}
}

// onOff is the word for running or paused.
func onOff(on bool, running, paused string) string {
	if on {
		return running
	}
	return paused
}

// adsState says whether new ads went up running or paused.
func adsState(made []network.Ad) string {
	for _, a := range made {
		if a.Active {
			return "ativos"
		}
	}
	return "pausados"
}
