package main

import (
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

func TestSvgCoordScales(t *testing.T) {
	// 2 points spanning min..max: first at bottom-left, last at top-right.
	first := svgCoord(0, 2, 0, 0, 100)
	if first.X != 10 || first.Y != 190 {
		t.Errorf("first = %+v, want x=10 y=190", first)
	}
	last := svgCoord(1, 2, 100, 0, 100)
	if last.X != 1190 || last.Y != 10 {
		t.Errorf("last = %+v, want x=1190 y=10", last)
	}
	// Flat series (max == min) lands mid-height without dividing by zero.
	if y := svgCoord(4, 9, 5, 5, 5).Y; y != 100 {
		t.Errorf("flat y = %v, want 100", y)
	}
	// Single point centers horizontally at the pad.
	if x := svgCoord(0, 1, 50, 0, 100).X; x != 10 {
		t.Errorf("single point x = %v, want 10", x)
	}
}

func TestPolylineAttr(t *testing.T) {
	got := polylineAttr([]svgPoint{{1, 2}, {3.25, 4.5}, {5, 6}})
	if got != "1.0,2.0 3.2,4.5 5.0,6.0" {
		t.Errorf("polylineAttr = %q", got)
	}
	if s := polylineAttr(nil); s != "" {
		t.Errorf("empty should be empty, got %q", s)
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

func TestFillMonthFlowsLinks(t *testing.T) {
	// Every padded row links to its own calendar month on /transactions.
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	out := fillMonthFlows([]store.MonthFlow{{Month: "2026-08", IncomeMinor: 1}}, 3, now)
	june := out[0] // June 2026
	if june.FromDate != "2026-06-01" || june.ToDate != "2026-06-30" {
		t.Errorf("june range = %s..%s", june.FromDate, june.ToDate)
	}
	if want := "/transactions?date_from=2026-06-01&date_to=2026-06-30"; string(june.TxURL) != want {
		t.Errorf("june url = %q, want %q", june.TxURL, want)
	}
	// Not all months have 30 days: August ends on the 31st.
	aug := out[2]
	if aug.FromDate != "2026-08-01" || aug.ToDate != "2026-08-31" {
		t.Errorf("august range = %s..%s", aug.FromDate, aug.ToDate)
	}
}

func TestComputeFlowDonut(t *testing.T) {
	rows := []monthFlowRow{
		{IncomeMinor: 800, ExpenseMinor: 400},
		{IncomeMinor: 200, ExpenseMinor: 0},
	}
	d := computeFlowDonut(rows)
	if !d.HasData || d.IncomeMinor != 1000 || d.ExpenseMinor != 400 {
		t.Errorf("totals wrong: %+v", d)
	}
	if d.NetMinor != 600 || d.TotalMinor != 1400 {
		t.Errorf("net/total wrong: %+v", d)
	}
	// Income is 1000/1400 ≈ 71.4% → integer 71; expense takes the remainder
	// so the arcs always tile exactly (no rounding gap): 71 + 29 = 100.
	if d.IncomePct != 71 || d.ExpensePct != 29 {
		t.Errorf("pcts = %d/%d, want 71/29", d.IncomePct, d.ExpensePct)
	}

	// Zero movement → no arcs at all.
	if d := computeFlowDonut(nil); d.HasData || d.TotalMinor != 0 {
		t.Errorf("empty donut should be inert: %+v", d)
	}

	// Expense-only window: the income arc disappears, expense fills the ring.
	d = computeFlowDonut([]monthFlowRow{{ExpenseMinor: 50}})
	if !d.HasData || d.IncomePct != 0 || d.ExpensePct != 100 || d.NetMinor != -50 {
		t.Errorf("expense-only donut wrong: %+v", d)
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
