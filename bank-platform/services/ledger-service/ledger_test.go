package main

import (
	"testing"

	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	"github.com/dso202/bank-platform/pkg/ledgerx"
)

func TestValidatePostings(t *testing.T) {
	cases := []struct {
		name string
		ps   []*ledgerv1.Posting
		ok   bool
	}{
		{"balanced pair", ledgerx.Move("a", "b", 500), true},
		{"single posting", []*ledgerv1.Posting{{AccountId: "a", Direction: "DEBIT", Amount: 1}}, false},
		{"unbalanced", []*ledgerv1.Posting{
			{AccountId: "a", Direction: "DEBIT", Amount: 100},
			{AccountId: "b", Direction: "CREDIT", Amount: 99},
		}, false},
		{"zero amount", []*ledgerv1.Posting{
			{AccountId: "a", Direction: "DEBIT", Amount: 0},
			{AccountId: "b", Direction: "CREDIT", Amount: 0},
		}, false},
		{"negative amount", []*ledgerv1.Posting{
			{AccountId: "a", Direction: "DEBIT", Amount: -5},
			{AccountId: "b", Direction: "CREDIT", Amount: -5},
		}, false},
		{"bad direction", []*ledgerv1.Posting{
			{AccountId: "a", Direction: "UP", Amount: 5},
			{AccountId: "b", Direction: "CREDIT", Amount: 5},
		}, false},
		{"missing account", []*ledgerv1.Posting{
			{Direction: "DEBIT", Amount: 5},
			{AccountId: "b", Direction: "CREDIT", Amount: 5},
		}, false},
		{"three-way split (loan EMI)", []*ledgerv1.Posting{
			{AccountId: "cust", Direction: "DEBIT", Amount: 1000},
			{AccountId: ledgerx.LoanPortfolio, Direction: "CREDIT", Amount: 900},
			{AccountId: ledgerx.InterestIncome, Direction: "CREDIT", Amount: 100},
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePostings(c.ps)
			if (err == nil) != c.ok {
				t.Fatalf("validatePostings() err = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestMoveIsBalanced(t *testing.T) {
	ps := ledgerx.Move("from", "to", 12345)
	if err := validatePostings(ps); err != nil {
		t.Fatal(err)
	}
	if ps[0].Direction != ledgerx.Debit || ps[0].AccountId != "from" || ps[1].Direction != ledgerx.Credit || ps[1].AccountId != "to" {
		t.Fatalf("unexpected postings %+v", ps)
	}
}

func TestBalanceAfter(t *testing.T) {
	e := &ledgerv1.JournalEntry{Postings: []*ledgerv1.Posting{
		{AccountId: "a", BalanceAfter: 10},
		{AccountId: "b", BalanceAfter: 20},
	}}
	if got := ledgerx.BalanceAfter(e, "b"); got != 20 {
		t.Fatalf("BalanceAfter = %d, want 20", got)
	}
}
