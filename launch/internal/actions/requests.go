package actions

import (
	"context"
	"encoding/json"
	"errors"

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
	r, err := l.st.Request(ctx, id)
	if err != nil {
		return r, nil, err
	}
	var in RequestInput
	if err := json.Unmarshal(r.Input, &in); err != nil {
		return r, nil, &network.Refused{Message: "o pedido veio num formato que o Launch não lê"}
	}
	if r, err = l.st.Decide(ctx, id, "confirmed", person); err != nil {
		return r, nil, err
	}
	who := Who{Person: person, AskedBy: r.Origin}
	var done []Done
	switch r.Kind {
	case "pause":
		done, err = l.Pause(ctx, who, in.Network, in.Account, in.Campaigns)
	case "pause_ads":
		if len(in.Campaigns) != 1 {
			err = &network.Refused{Message: "pausar anúncios vale para uma campanha por vez"}
			break
		}
		done, err = l.PauseAds(ctx, who, in.Network, in.Account, in.Campaigns[0], in.Ads)
	case "change":
		if in.Change == nil {
			err = &network.Refused{Message: "o pedido não diz o que mudar"}
			break
		}
		done, err = l.Change(ctx, who, in.Network, in.Account, in.Campaigns, *in.Change)
	case "duplicate":
		done, err = l.Duplicate(ctx, who, in.Network, in.Account, in.Campaigns)
	case "move":
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

// Refuse is a person saying no to a request; nothing is sent.
func (l *Launch) Refuse(ctx context.Context, person string, id int64) (store.Request, error) {
	return l.st.Decide(ctx, id, "refused", person)
}

// IsDecided reports a request someone already confirmed or refused.
func IsDecided(err error) bool { return errors.Is(err, store.ErrDecided) }
