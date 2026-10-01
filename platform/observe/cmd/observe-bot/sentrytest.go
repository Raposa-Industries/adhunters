package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Raposa-Industries/adhunters/kit/errs"
	"github.com/Raposa-Industries/adhunters/kit/logx"
)

// sentryTestCmd sends one error through the same path every service uses
// (logx, kit/errs), so a new SENTRY_DSN can be tried without breaking
// anything. The issue it makes is safe to resolve.
//
// Sentry groups events by service and log message, and the relay only posts
// an issue the first time it is seen, so the message carries the time: each
// run is a new issue and reaches the Telegram group again.
func sentryTestCmd() error {
	if os.Getenv("SENTRY_DSN") == "" {
		return errors.New("SENTRY_DSN is not set (it lives in /etc/adhunters/observe.env)")
	}
	log := logx.New("observe-bot", version)
	if !errs.Enabled() {
		return errors.New("Sentry did not start; the line above says why")
	}
	msg := "sentry test from observe-bot at " + time.Now().UTC().Format(time.RFC3339)
	log.Error(msg, "err", errors.New("a test error, safe to resolve"))
	errs.Flush(10 * time.Second)
	fmt.Printf("sent. In Sentry it is the new issue %q (if it is not there within a minute, the DSN is wrong);\n", msg)
	fmt.Println("once observe-bot runs with SENTRY_API_TOKEN, the Telegram group gets it within a minute or two.")
	return nil
}
