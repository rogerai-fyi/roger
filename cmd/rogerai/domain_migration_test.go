package main

import (
	"strings"
	"testing"
)

func TestNewInstallUsesFMWhileLegacyBrokerRemainsValid(t *testing.T) {
	if defaultBroker != "https://broker.rogerai.fm" {
		t.Fatalf("new-install broker = %q, want branded .fm host", defaultBroker)
	}
	if !validBrokerURL("https://broker.rogerai.fyi") {
		t.Fatal("legacy .fyi broker must remain a valid configured broker")
	}
}

func TestNewRemoteLinksUseFM(t *testing.T) {
	got := rcLinkURL("8FK3-9MQ2")
	if !strings.HasPrefix(got, "https://rogerai.fm/r.html#") {
		t.Fatalf("remote link = %q, want canonical .fm URL", got)
	}
}
