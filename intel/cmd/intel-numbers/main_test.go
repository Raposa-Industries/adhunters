package main

import "testing"

func TestOpsChatFallsBackToTheAlertsGroup(t *testing.T) {
	t.Setenv("TELEGRAM_CHAT_ID", "-1")
	t.Setenv("OPS_TELEGRAM_CHAT_ID", "")
	if got := opsChat(); got != "-1" {
		t.Fatalf("without the ops group: %q, want -1", got)
	}
	t.Setenv("OPS_TELEGRAM_CHAT_ID", "-2")
	if got := opsChat(); got != "-2" {
		t.Fatalf("with the ops group: %q, want -2", got)
	}
}
