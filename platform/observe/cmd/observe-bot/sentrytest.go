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
func sentryTestCmd() error {
	if os.Getenv("SENTRY_DSN") == "" {
		return errors.New("SENTRY_DSN is not set (it lives in /etc/adhunters/observe.env)")
	}
	log := logx.New("observe-bot", version)
	if !errs.Enabled() {
		return errors.New("Sentry did not start; the line above says why")
	}
	log.Error("sentry test from observe-bot", "err", errors.New("a test error, safe to resolve"))
	errs.Flush(10 * time.Second)
	fmt.Println("sent. In Sentry it is the issue \"sentry test from observe-bot\" (if it is not there within a minute, the DSN is wrong);")
	fmt.Println("once observe-bot runs with SENTRY_API_TOKEN, the Telegram group gets it too.")
	return nil
}
