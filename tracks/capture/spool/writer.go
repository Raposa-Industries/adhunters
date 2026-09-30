package spool

import (
	"log/slog"

	rawspool "github.com/Raposa-Industries/adhunters/shared/spool"
)

// Sealed describes one raw file that was closed and compressed. Its Stream
// is the network.
type Sealed = rawspool.Sealed

// Options tune a Writer. Zero values are the production defaults.
type Options = rawspool.Options

// Writer writes records to the open minute's file of their network and seals
// each file when its minute ends (shared/spool does the files).
type Writer struct{ *rawspool.Writer }

// Open starts a writer under dir for instance. Files a previous run left
// unsealed (it crashed, or was killed) are sealed first.
func Open(dir, instance string, log *slog.Logger, opt Options) (*Writer, error) {
	w, err := rawspool.Open(dir, "capture", instance, log, opt)
	if err != nil {
		return nil, err
	}
	return &Writer{w}, nil
}

// Write appends one record to its network's open file.
func (w *Writer) Write(rec *Record) error {
	line, err := rec.Marshal()
	if err != nil {
		return err
	}
	return w.WriteLine(rec.Network, line)
}
