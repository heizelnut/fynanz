package main

import (
	"strings"
	"testing"
	"time"

	"fynanz/internal/store"
)

func TestDeriveBalanceSeries(t *testing.T) {
	// Unit consistency: everything in minor units. Current balance 10000
	// (100.00). Movements ascending: +1000, -2000, +3000.
	// balance(d) = current - Σ(movements strictly after d):
	//   d2 (2026-01-03): 10000
	//   d1 (2026-01-02): 10000 - 3000        = 7000
	//   d0 (2026-01-01): 10000 - 3000 + 2000  = 9000
	days := []store.DailyAmount{
		{Date: "2026-01-01", AmountMinor: 1000},
		{Date: "2026-01-02", AmountMinor: -2000},
		{Date: "2026-01-03", AmountMinor: 3000},
	}
	pts := deriveBalanceSeries(10000, days, 60)
	if len(pts) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(pts), pts)
	}
	if pts[0].Minor != 9000 || pts[1].Minor != 7000 || pts[2].Minor != 10000 {
		t.Errorf("balances = %d,%d,%d, want 9000,7000,10000", pts[0].Minor, pts[1].Minor, pts[2].Minor)
	}
	if pts[0].Date != "2026-01-01" || pts[2].Date != "2026-01-03" {
		t.Errorf("dates wrong: %+v", pts)
	}

	// Downsampling keeps endpoints and spreads evenly.
	big := make([]store.DailyAmount, 100)
	for i := range big {
		big[i] = store.DailyAmount{Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02"), AmountMinor: 1}
	}
	pts = deriveBalanceSeries(0, big, 10)
	if len(pts) != 10 || pts[0].Minor != -99 || pts[9].Minor != 0 {
		t.Errorf("downsample wrong: len=%d first=%d last=%d", len(pts), pts[0].Minor, pts[9].Minor)
	}

	// Empty input → nil.
	if pts := deriveBalanceSeries(5, nil, 10); pts != nil {
		t.Errorf("empty input should be nil: %+v", pts)
	}
}

func TestSvgPointsScales(t *testing.T) {
	pts := []balancePoint{{Date: "a", Minor: 0}, {Date: "b", Minor: 100}}
	s := svgPoints(pts, 0, 100, 600, 200)
	if !strings.HasPrefix(s, "10.0,") || !strings.HasSuffix(s, "590.0,10.0") {
		t.Errorf("scaled points wrong: %q", s)
	}
	// Flat series lands mid-height and doesn't divide by zero.
	flat := svgPoints([]balancePoint{{Minor: 5}, {Minor: 5}}, 5, 5, 600, 200)
	if !strings.Contains(flat, "100.0") {
		t.Errorf("flat series should sit mid-height: %q", flat)
	}
}

func TestFillMonthFlows(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	rows := []store.MonthFlow{
		{Month: "2026-08", IncomeMinor: 800, ExpenseMinor: 400},
		{Month: "2026-06", IncomeMinor: 200, ExpenseMinor: 0},
	}
	out := fillMonthFlows(rows, 6, now)
	if len(out) != 6 {
		t.Fatalf("len = %d, want 6", len(out))
	}
	// March (2026-03) is the oldest slot; Aug last; labels short.
	if out[0].Month != "2026-03" || out[5].Month != "2026-08" || out[5].Label != "Aug" {
		t.Errorf("months wrong: %+v", out)
	}
	// Zero months filled (April, May, July have zeros); June carried.
	if out[1].Month != "2026-04" || out[1].IncomeMinor != 0 {
		t.Errorf("april should be zero-filled: %+v", out[1])
	}
	if out[3].Month != "2026-06" || out[3].IncomeMinor != 200 {
		t.Errorf("june not carried: %+v", out[3])
	}
	if out[5].IncomePct != 100 || out[5].ExpensePct != 50 {
		t.Errorf("pct wrong: %+v", out[5])
	}
}

func TestFillCatSpend(t *testing.T) {
	rows := []store.CategorySpend{
		{Name: "Groceries", TotalMinor: 1000},
		{Name: "Transport", TotalMinor: 250},
	}
	out := fillCatSpend(rows)
	if len(out) != 2 || out[0].Pct != 100 || out[1].Pct != 25 {
		t.Errorf("cat spend wrong: %+v", out)
	}
}
