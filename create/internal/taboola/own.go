package taboola

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ownState is what a client with OnlyOwn created, saved in StateFile after
// every create so a restart still knows it. Keys are Taboola ids, values the
// account they are in.
type ownState struct {
	Campaigns map[string]string `json:"campaigns"`
	Groups    map[string]string `json:"groups"`
}

func (c *Client) loadOwn() error {
	if c.s.StateFile == "" {
		return errors.New("taboola: only-own needs a state file")
	}
	c.own = ownState{Campaigns: map[string]string{}, Groups: map[string]string{}}
	b, err := os.ReadFile(c.s.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("taboola: state file: %w", err)
	}
	if err := json.Unmarshal(b, &c.own); err != nil {
		return fmt.Errorf("taboola: state file %s: %w", c.s.StateFile, err)
	}
	if c.own.Campaigns == nil {
		c.own.Campaigns = map[string]string{}
	}
	if c.own.Groups == nil {
		c.own.Groups = map[string]string{}
	}
	return nil
}

// ownsCampaign reports whether campaign id in account is one this client
// created. Without OnlyOwn every campaign counts as its own.
func (c *Client) ownsCampaign(account, id string) bool {
	if !c.s.OnlyOwn {
		return true
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return id != "" && c.own.Campaigns[id] == account
}

func (c *Client) ownsGroup(account, id string) bool {
	if !c.s.OnlyOwn {
		return true
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return id != "" && c.own.Groups[id] == account
}

// checkOwnCampaign refuses a campaign this client did not create.
func (c *Client) checkOwnCampaign(account, id string) error {
	if !c.ownsCampaign(account, id) {
		return refuse("conta de testes: a campanha %s não foi criada aqui e não pode ser usada", oneLine(id, 30))
	}
	return nil
}

// checkOwnName refuses a name without the prefix, when one is set. The
// prefix is optional: what keeps a lent account safe is the state file,
// since only what is recorded there can be read or changed.
func (c *Client) checkOwnName(name, what string) error {
	if c.s.OnlyOwn && c.s.NamePrefix != "" && !strings.HasPrefix(strings.TrimSpace(name), c.s.NamePrefix) {
		return refuse("conta de testes: o nome %s deve começar com %q", what, c.s.NamePrefix)
	}
	return nil
}

// checkOwnPath is the OnlyOwn half of the guard in do: under an account it
// lets through only the lists, the creates, and paths under a campaign or
// group this client created.
func (c *Client) checkOwnPath(account, rest string) error {
	parts := strings.Split(rest, "/")
	kind, id := parts[0], ""
	if len(parts) > 1 {
		id = parts[1]
	}
	switch {
	case (kind == "campaigns" || kind == "campaigns_group") && id == "" && len(parts) <= 2:
		return nil
	case kind == "campaigns":
		return c.checkOwnCampaign(account, id)
	case kind == "campaigns_group" && c.ownsGroup(account, id):
		return nil
	}
	return refuse("conta de testes: %s não foi criado aqui", oneLine(rest, 60))
}

// remember records a campaign or group this client created, and saves the
// state at once (written whole, then renamed). Without OnlyOwn it does
// nothing.
func (c *Client) remember(account, campaign, group string) error {
	if !c.s.OnlyOwn {
		return nil
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if campaign != "" {
		c.own.Campaigns[campaign] = account
	}
	if group != "" {
		c.own.Groups[group] = account
	}
	b, err := json.MarshalIndent(c.own, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.s.StateFile), 0o755); err != nil {
		return err
	}
	tmp := c.s.StateFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, c.s.StateFile)
}

// rememberOrSay records what was created; when that fails the thing still
// exists on Taboola, so the error names it.
func (c *Client) rememberOrSay(account, campaign, group string) error {
	if err := c.remember(account, campaign, group); err != nil {
		c.log.Error("taboola state not saved", "campaign", campaign, "group", group, "err", err)
		what := "campanha " + campaign
		if group != "" {
			what = "grupo " + group
		}
		return &Error{Message: what + " criado na Taboola, mas não consegui anotá-lo no arquivo de estado; ele não vai aparecer aqui"}
	}
	return nil
}
