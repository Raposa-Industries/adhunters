package actions

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Raposa-Industries/adhunters/launch/internal/network"
)

// The team's names (2026-09-30): groups are numbered 01, 02, … and
// campaigns CMP<n>-<account number>-<Desktop|Mobile>-pp-bl, both counted
// per account from the highest already there. A campaign made for both
// devices is two campaigns with the same number.

var (
	groupNumber    = regexp.MustCompile(`^\s*(\d{1,6})\s*$`)
	campaignNumber = regexp.MustCompile(`(?i)^\s*CMP(\d{1,6})-`)
	accountDigits  = regexp.MustCompile(`\d+`)
)

// NextGroupName is the next free group number in groups, two digits at
// least: "01" in an account with none.
func NextGroupName(groups []network.Group) string {
	top := 0
	for _, g := range groups {
		if m := groupNumber.FindStringSubmatch(g.Name); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > top {
				top = n
			}
		}
	}
	return fmt.Sprintf("%02d", top+1)
}

// NextCampaignNumber is the next CMP number in campaigns.
func NextCampaignNumber(campaigns []network.Campaign) int {
	top := 0
	for _, c := range campaigns {
		if m := campaignNumber.FindStringSubmatch(c.Name); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > top {
				top = n
			}
		}
	}
	return top + 1
}

// AccountNumber is the number that stands for an account in campaign
// names: the first number in its id (zoltagroup-1-sc is 1), or else its
// place in the login's accounts (1 for the first).
func AccountNumber(account string, accounts []network.Account) string {
	if n := accountDigits.FindString(account); n != "" {
		return n
	}
	for i, a := range accounts {
		if a.ID == account {
			return strconv.Itoa(i + 1)
		}
	}
	return "1"
}

// CampaignName is the team's name for one campaign.
func CampaignName(number int, accountNumber string, d network.Device) string {
	dev := "Desktop"
	if d == network.Mobile {
		dev = "Mobile"
	}
	return fmt.Sprintf("CMP%02d-%s-%s-pp-bl", number, accountNumber, dev)
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
