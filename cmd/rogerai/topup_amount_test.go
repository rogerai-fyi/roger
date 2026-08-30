package main

import (
	"testing"

	"rogerai.fm/roger/v6/internal/client"
)

// The retained `balance --topup` aliases must read an amount exactly the way the
// documented `roger topup <amt>` does, and must refuse exactly what it refuses. They
// used to be the ones that got "$25" right while the documented verb silently charged
// $10; after that was fixed the risk ran the other way, with the alias swallowing the
// parser's error and charging the default on an amount the documented verb rejects.
//
// Both now read through client.ParseTopupAmount, so this asserts the agreement rather
// than re-testing the parser (internal/client/topup_amount_test.go owns that).
func TestBalanceTopupAliasAgreesWithTheDocumentedVerb(t *testing.T) {
	for _, amount := range []string{"25", "$25", "12.50", " 40 "} {
		want, err := client.ParseTopupAmount([]string{amount})
		if err != nil {
			t.Fatalf("ParseTopupAmount(%q) errored: %v", amount, err)
		}
		for _, args := range [][]string{
			{"topup", amount},
			{"--topup", amount},
			{"--topup=" + amount},
		} {
			got, matched, err := balanceTopupAlias(args)
			if !matched {
				t.Errorf("balanceTopupAlias(%q) did not recognize a top-up", args)
				continue
			}
			if err != nil {
				t.Errorf("balanceTopupAlias(%q) refused an amount the documented verb accepts: %v", args, err)
				continue
			}
			if got != want {
				t.Errorf("balanceTopupAlias(%q) = $%v, but `roger topup %s` = $%v", args, got, amount, want)
			}
		}
	}
}

// A refusal has to travel. The alias matched, so `balance topup bogus` is unambiguously
// a top-up request - and an unreadable amount on that path must stop the command, not
// open checkout for the default.
func TestBalanceTopupAliasPropagatesARefusal(t *testing.T) {
	for _, args := range [][]string{
		{"topup", "bogus"},
		{"--topup", "twentyfive"},
		{"--topup=0"},
		{"topup", "-5"},
		{"--topup=NaN"},
	} {
		_, matched, err := balanceTopupAlias(args)
		if !matched {
			t.Errorf("balanceTopupAlias(%q) did not recognize a top-up", args)
			continue
		}
		if err == nil {
			t.Errorf("balanceTopupAlias(%q) accepted an unreadable amount instead of refusing it", args)
		}
	}
}
