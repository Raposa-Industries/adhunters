package actions

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Raposa-Industries/adhunters/launch/internal/network"
)

// The team's names (2026-10-02, Draw Designer d587e1b829), each level
// starting with the one above it:
//
//	group     GRP03
//	campaign  GRP03-CMP01-Desk-pp-bl, GRP03-CMP01-Mobile-pp-bl
//	ad        GRP03-CMP01-AD01-Desk-pp-bl
//
// Groups are counted per account and campaigns per group, from the highest
// already there. The names from before (groups 01, 02… and campaigns
// CMP<n>-<account number>-<Desktop|Mobile>-pp-bl) still count, so a new
// group after "07" is GRP08. A campaign made for both devices is two
// campaigns with the same number. Taboola's items have no name, so an ad's
// name lives only in Launch (History, the steps' preview, the bulk sheet).

var (
	groupNumber    = regexp.MustCompile(`(?i)^\s*(?:GRP)?(\d{1,6})\s*$`)
	campaignNumber = regexp.MustCompile(`(?i)(?:^|-)CMP(\d{1,6})-`)
	deviceTail     = regexp.MustCompile(`(?i)-(Desk|Desktop|Mobile)-pp-bl$`)
	adNumber       = regexp.MustCompile(`-(AD\d+)`)
)

// NextGroupName is the next free group name in groups: GRP01 in an account
// with none.
func NextGroupName(groups []network.Group) string {
	top := 0
	for _, g := range groups {
		if m := groupNumber.FindStringSubmatch(g.Name); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > top {
				top = n
			}
		}
	}
	return fmt.Sprintf("GRP%02d", top+1)
}

// GroupPrefix is what a group's campaigns start with: GRP<nn> for a
// numbered group (GRP03, or 03 from before), else the group's own name.
func GroupPrefix(name string) string {
	if m := groupNumber.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return fmt.Sprintf("GRP%02d", n)
	}
	return strings.Join(strings.Fields(name), " ")
}

// NextCampaignNumber is the next CMP number among the campaigns of group:
// 1 for a group without any (or a new group, "").
func NextCampaignNumber(campaigns []network.Campaign, group string) int {
	top := 0
	if group == "" {
		return 1
	}
	for _, c := range campaigns {
		if c.GroupID != group {
			continue
		}
		if m := campaignNumber.FindStringSubmatch(c.Name); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > top {
				top = n
			}
		}
	}
	return top + 1
}

// CampaignName is the team's name for one campaign of a group.
func CampaignName(prefix string, number int, d network.Device) string {
	return fmt.Sprintf("%s-CMP%02d-%s-pp-bl", prefix, number, deviceWord(d))
}

func deviceWord(d network.Device) string {
	if d == network.Mobile {
		return "Mobile"
	}
	return "Desk"
}

// AdName is the team's name for the n-th ad (from 1) of a campaign: AD<nn>
// before the campaign's device ending (GRP01-CMP01-AD01-Desk-pp-bl), or
// after the whole name when it has none.
func AdName(campaign string, n int) string {
	campaign = strings.TrimSpace(campaign)
	if loc := deviceTail.FindStringIndex(campaign); loc != nil {
		return fmt.Sprintf("%s-AD%02d%s", campaign[:loc[0]], n, campaign[loc[0]:])
	}
	return fmt.Sprintf("%s-AD%02d", campaign, n)
}

// nameAds gives each made ad its name in campaign, in the order the ads
// were asked for (made ads are matched to the asked ones by our ad id).
func nameAds(campaign string, asked []network.NewAd, made []network.Ad) {
	used := make([]bool, len(asked))
	for i := range made {
		for k, a := range asked {
			if !used[k] && a.AdID == made[i].AdID {
				used[k] = true
				made[i].Name = AdName(campaign, k+1)
				break
			}
		}
	}
}

// adNames says the names of ads made in campaign, for History: "GRP01-CMP01-AD01-Desk-pp-bl"
// or "GRP01-CMP01-AD01-Desk-pp-bl a AD08".
func adNames(made []network.Ad) string {
	var first, last string
	for _, a := range made {
		if a.Name == "" {
			continue
		}
		if first == "" {
			first = a.Name
		}
		last = a.Name
	}
	if first == "" || first == last {
		return first
	}
	if m := adNumber.FindAllStringSubmatch(last, -1); m != nil {
		return first + " a " + m[len(m)-1][1]
	}
	return first + " a " + last
}

// Devices reads a request's devices: desktop, mobile or both (the default).
func Devices(s string) ([]network.Device, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "both":
		return []network.Device{network.Desktop, network.Mobile}, nil
	case "desktop":
		return []network.Device{network.Desktop}, nil
	case "mobile":
		return []network.Device{network.Mobile}, nil
	}
	return nil, &network.Refused{Message: "dispositivo deve ser desktop, mobile ou os dois"}
}
