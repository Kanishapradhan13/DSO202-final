package main

import (
	"testing"
	"time"
)

func TestAmortizeRepaysExactPrincipal(t *testing.T) {
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		principal int64
		bps       int32
		months    int32
	}{
		{10_000_000, 1300, 24},
		{5_000_000, 900, 12},
		{1_000_000, 1300, 6},
		{123_456_789, 950, 240},
		{1_000_000, 0, 10},
	} {
		emi, interest, sched := amortize(c.principal, c.bps, c.months, start)
		if len(sched) != int(c.months) {
			t.Fatalf("schedule has %d rows, want %d", len(sched), c.months)
		}
		var principalSum, interestSum int64
		for i, in := range sched {
			principalSum += in.Principal
			interestSum += in.Interest
			if in.Amount != in.Principal+in.Interest {
				t.Fatalf("row %d: amount %d != principal %d + interest %d", i, in.Amount, in.Principal, in.Interest)
			}
			if i < len(sched)-1 && in.Amount != emi {
				t.Fatalf("row %d: amount %d != emi %d", i, in.Amount, emi)
			}
		}
		if principalSum != c.principal {
			t.Fatalf("principal repaid %d != lent %d", principalSum, c.principal)
		}
		if interestSum != interest {
			t.Fatalf("interest sum %d != reported %d", interestSum, interest)
		}
		if last := sched[len(sched)-1]; last.BalanceAfter != 0 {
			t.Fatalf("final balance %d, want 0", last.BalanceAfter)
		}
		if !sched[0].DueDate.Equal(start.AddDate(0, 1, 0)) {
			t.Fatalf("first due date %v", sched[0].DueDate)
		}
	}
}

func TestAmortizeKnownEMI(t *testing.T) {
	// Nu. 100,000 at 12% p.a. over 12 months: EMI = Nu. 8,884.88.
	emi, _, _ := amortize(10_000_000, 1200, 12, time.Now())
	if emi != 888_488 {
		t.Fatalf("emi = %d, want 888488", emi)
	}
}
