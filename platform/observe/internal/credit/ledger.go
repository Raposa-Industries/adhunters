package credit

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Ledger is where an estimate stands: the balance and moment it started from,
// what was spent since, and up to when the spending has been counted. It is
// kept on disk so a restart neither counts a stretch twice nor skips one.
type Ledger struct {
	Balance float64   `json:"balance"`
	AsOf    time.Time `json:"as_of"`
	Spent   float64   `json:"spent"`
	Through time.Time `json:"through"`
}

// Remaining is the estimate: the balance less what was spent since.
func (l Ledger) Remaining() float64 { return l.Balance - l.Spent }

// Start returns l when it still counts from e's balance, or a fresh ledger
// when the owner has put in a new balance or moment (or there was none).
func (l Ledger) Start(e Estimate) Ledger {
	if l.Balance == e.Balance && l.AsOf.Equal(e.AsOf) && !l.Through.IsZero() {
		return l
	}
	return Ledger{Balance: e.Balance, AsOf: e.AsOf, Through: e.AsOf}
}

// LedgerPath is where the ledger of estimate name lives in dir.
func LedgerPath(dir, name string) string {
	return filepath.Join(dir, "estimate-"+name+".json")
}

// ReadLedger reads a ledger; a missing file is an empty ledger.
func ReadLedger(path string) (Ledger, error) {
	var l Ledger
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	return l, json.Unmarshal(b, &l)
}

// WriteLedger replaces the ledger at path in one step.
func WriteLedger(path string, l Ledger) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
