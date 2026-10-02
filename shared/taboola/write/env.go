package write

import (
	"fmt"
	"strconv"
	"strings"
)

// SettingsFromEnv reads a Taboola login from the environment (getenv is
// os.Getenv outside tests). Unset credentials leave the client off; a
// network account or a bad ceiling is an error, so the binary refuses to
// start rather than run with a guard it did not mean.
//
//	TABOOLA_CLIENT_ID, TABOOLA_CLIENT_SECRET   one login's Backstage keys
//	TABOOLA_ACCOUNTS       advertiser accounts it may use, comma separated
//	TABOOLA_BASE_URL       DefaultBase (a local fake, for trying)
//	TABOOLA_MAX_CPC        1.00  highest bid in USD
//	TABOOLA_MAX_DAILY_CAP  20    highest daily cap in USD
//	TABOOLA_MAX_SPEND_LIMIT 20   highest total (lifetime) spend of one campaign
//	                       in USD; every campaign made or copied gets one.
//	                       0 is no spending limit (the owner, 2026-10-02): a
//	                       campaign gets none unless one is asked, and an
//	                       asked total is at most 30 daily caps
//	TABOOLA_CREATE_ACTIVE  1: new campaigns, groups and ads go up running
//	                       (the owner, 2026-10-02); copies stay paused
//	TABOOLA_ONLY_OWN       1 on a lent login: only what this server made
//	TABOOLA_NAME_PREFIX    optional, with only-own
//	TABOOLA_STATE_FILE     stateFile: what this server made, for only-own
func SettingsFromEnv(getenv func(string) string, stateFile string) (Settings, error) {
	or := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	s := Settings{
		Base:         or("TABOOLA_BASE_URL", DefaultBase),
		ClientID:     getenv("TABOOLA_CLIENT_ID"),
		ClientSecret: getenv("TABOOLA_CLIENT_SECRET"),
		NamePrefix:   strings.TrimSpace(getenv("TABOOLA_NAME_PREFIX")),
		StateFile:    or("TABOOLA_STATE_FILE", stateFile),
	}
	switch v := getenv("TABOOLA_CREATE_ACTIVE"); v {
	case "", "0", "false":
	case "1", "true":
		s.CreateActive = true
	default:
		return s, fmt.Errorf("TABOOLA_CREATE_ACTIVE must be 1 or 0, not %q", v)
	}
	switch v := getenv("TABOOLA_ONLY_OWN"); v {
	case "", "0", "false":
	case "1", "true":
		s.OnlyOwn = true
	default:
		return s, fmt.Errorf("TABOOLA_ONLY_OWN must be 1 or 0, not %q", v)
	}
	for _, a := range strings.Split(getenv("TABOOLA_ACCOUNTS"), ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if isNetwork(a) {
			return s, fmt.Errorf("TABOOLA_ACCOUNTS must hold advertiser accounts, not the network account %q", a)
		}
		s.Accounts = append(s.Accounts, a)
	}
	for _, l := range []struct {
		key    string
		def    float64
		to     *float64
		zeroOK bool // 0 means no ceiling
	}{
		{"TABOOLA_MAX_CPC", 1, &s.MaxCPC, false},
		{"TABOOLA_MAX_DAILY_CAP", 20, &s.MaxDailyCap, false},
		{"TABOOLA_MAX_SPEND_LIMIT", 20, &s.MaxSpendLimit, true},
	} {
		*l.to = l.def
		if v := getenv(l.key); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || !(f > 0 || l.zeroOK && f == 0) {
				if l.zeroOK {
					return s, fmt.Errorf("%s must be an amount in USD, 0 for none, not %q", l.key, v)
				}
				return s, fmt.Errorf("%s must be an amount in USD above 0, not %q", l.key, v)
			}
			*l.to = f
		}
	}
	return s, nil
}
