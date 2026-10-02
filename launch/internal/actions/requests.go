package actions

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Raposa-Industries/adhunters/launch/internal/network"
	"github.com/Raposa-Industries/adhunters/launch/internal/store"
)

// RequestInput is what launch_api.new_request_v1 takes as p_input.
type RequestInput struct {
	Network   string          `json:"network"`
	Account   string          `json:"account"`
	Campaigns []string        `json:"campaigns"`
	Ads       []string        `json:"ads,omitempty"`
	Change    *network.Change `json:"change,omitempty"`
	ToGroup   string          `json:"to_group,omitempty"`
	Originals string          `json:"originals,omitempty"`
}

// Confirm is a person confirming another service's request: Launch then
// makes the change, recorded in History as asked by the request's origin.
func (l *Launch) Confirm(ctx context.Context, person string, id int64) (store.Request, []Done, error) {
	r, err := l.st.Decide(ctx, id, "confirmed", person)
	if err != nil {
		return r, nil, err
	}
	who := Who{Person: person, AskedBy: r.Origin}
	var done []Done
	var in RequestInput
	if jerr := json.Unmarshal(r.Input, &in); jerr != nil {
		err = &network.Refused{Message: "o pedido veio num formato que o Launch não lê"}
	}
	switch {
	case err != nil:
	case r.Kind == "pause":
		done, err = l.Pause(ctx, who, in.Network, in.Account, in.Campaigns)
	case r.Kind == "pause_ads":
		if len(in.Campaigns) != 1 {
			err = &network.Refused{Message: "pausar anúncios vale para uma campanha por vez"}
			break
		}
		done, err = l.PauseAds(ctx, who, in.Network, in.Account, in.Campaigns[0], in.Ads)
	case r.Kind == "change":
		if in.Change == nil {
			err = &network.Refused{Message: "o pedido não diz o que mudar"}
			break
		}
		done, err = l.Change(ctx, who, in.Network, in.Account, in.Campaigns, *in.Change)
	case r.Kind == "duplicate":
		done, err = l.Duplicate(ctx, who, in.Network, in.Account, in.Campaigns)
	case r.Kind == "move":
		done, err = l.Move(ctx, who, MoveRequest{Network: in.Network, Account: in.Account, Campaigns: in.Campaigns, ToGroup: in.ToGroup, Originals: in.Originals})
	default:
		err = &network.Refused{Message: "pedido de um tipo que o Launch não conhece: " + r.Kind}
	}
	state, result := "sent", map[string]any{"done": done}
	if err != nil {
		state, result = "failed", map[string]any{"error": l.Say(err)}
	} else if len(done) > 0 && allFailed(done) {
		state = "failed"
	}
	if ferr := l.st.Finish(ctx, id, state, result); ferr != nil {
		l.log.Error("request result not recorded", "request", id, "state", state, "err", ferr)
	}
	r.State = state
	return r, done, err
}

func allFailed(done []Done) bool {
	for _, d := range done {
		if d.Error == "" {
			return false
		}
	}
	return true
}

// RequestMaxAge is how long a request may wait to be carried out; an
// older one is refused instead (Launch was down that long, say).
const RequestMaxAge = time.Hour

// RunWaiting carries out the requests waiting in launch.request, oldest
// first, each as the person who asked: Desk sends a request only after
// that person said yes in its conversation (owner, 2026-10-02), so Launch
// has no screen of its own to confirm them. Each one goes through the same
// actions, limits and rules as the app, and none of them turns anything on.
// A request that fails is recorded failed; only reading or recording the
// requests returns an error. It says how many it carried out.
func (l *Launch) RunWaiting(ctx context.Context) (int, error) {
	if _, err := l.st.Expire(ctx, RequestMaxAge, "launch", "o pedido esperou mais de uma hora e não foi feito; peça de novo"); err != nil {
		return 0, err
	}
	rs, err := l.st.WaitingRequests(ctx, RequestMaxAge, 20)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rs {
		person := r.RequestedBy
		if person == "" {
			person = r.Origin
		}
		got, _, err := l.Confirm(ctx, person, r.ID)
		switch {
		case IsDecided(err):
			continue
		case err != nil:
			l.log.Warn("request failed", "request", r.ID, "kind", r.Kind, "origin", r.Origin, "err", err)
		default:
			l.log.Info("request carried out", "request", r.ID, "kind", r.Kind, "origin", r.Origin, "state", got.State)
		}
		n++
	}
	return n, nil
}

// Refuse is a person saying no to a request; nothing is sent.
func (l *Launch) Refuse(ctx context.Context, person string, id int64) (store.Request, error) {
	return l.st.Decide(ctx, id, "refused", person)
}

// IsDecided reports a request someone already confirmed or refused.
func IsDecided(err error) bool { return errors.Is(err, store.ErrDecided) }
