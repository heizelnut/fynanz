package main

import (
	"fmt"
	"strings"
	"time"

	"fynanz/internal/store"
)

// balancePoint is one point of a derived balance series.
type balancePoint struct {
	Date  string // YYYY-MM-DD
	Minor int64
}

// deriveBalanceSeries reconstructs a per-day balance history by walking
// backwards from the account's current fetched balance using daily movement
// sums (all transactions, including internal transfers — balance is balance):
//
//	balance(date) = current - Σ(movements strictly after date)
//
// The result is ascending and capped at maxPoints (evenly spread, keeping the
// newest point; callers label the result as derived, not bank-reported).
func deriveBalanceSeries(current int64, days []store.DailyAmount, maxPoints int) []balancePoint {
	if len(days) == 0 {
		return nil
	}
	pts := make([]balancePoint, 0, len(days))
	running := current
	pts = append(pts, balancePoint{Date: days[len(days)-1].Date, Minor: running})
	for i := len(days) - 2; i >= 0; i-- {
		running -= days[i+1].AmountMinor
		pts = append(pts, balancePoint{Date: days[i].Date, Minor: running})
	}
	// Reverse into ascending order (in place).
	for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
		pts[i], pts[j] = pts[j], pts[i]
	}
	if len(pts) <= maxPoints {
		return pts
	}
	step := float64(len(pts)-1) / float64(maxPoints-1)
	out := make([]balancePoint, 0, maxPoints)
	for k := 0; k < maxPoints; k++ {
		i := int(float64(k)*step + 0.5)
		out = append(out, pts[i])
	}
	out[len(out)-1] = pts[len(pts)-1] // always keep the newest point
	return out
}

// lineSeries is one scaled polyline for the balance SVG.
type lineSeries struct {
	Label  string
	Points string
	Class  string // bal-0, bal-1… (stroke colors in CSS)
}

// balChartJSON is what the crosshair cursor consumes (server-emitted, so the
// client never re-implements scaling logic).
type balChartJSON struct {
	Currency string          `json:"currency"`
	Min      int64           `json:"min"`
	Max      int64           `json:"max"`
	Series   []balSeriesJSON `json:"series"`
}

type balSeriesJSON struct {
	Label  string         `json:"label"`
	Points []balPointJSON `json:"points"`
}

type balPointJSON struct {
	Date  string `json:"date"`
	Minor int64  `json:"minor"`
}

// balanceChart is the fully prepared SVG description.
type balanceChart struct {
	Min, Max int64
	Series   []lineSeries
	Ticks    []string // x-axis date labels (first, middle, last)
}

// buildBalanceChart scales all series into one viewBox 600x200 space with
// 10px padding, sharing the global min/max so series are comparable.
func buildBalanceChart(series []lineSeries, min, max int64, ticks []string) balanceChart {
	return balanceChart{Min: min, Max: max, Series: series, Ticks: ticks}
}

// svgPoints converts balance points to a "x,y x,y …" polyline string using
// the global min/max, in a viewBox of width×height with 10px margins.
func svgPoints(pts []balancePoint, minVal, maxVal int64, w, h int) string {
	const pad = 10.0
	var b strings.Builder
	n := len(pts)
	for i, p := range pts {
		x := pad + float64(i)*((float64(w)-2*pad)/float64(max(1, n-1)))
		y := 0.0
		if maxVal > minVal {
			y = pad + (float64(maxVal)-float64(p.Minor))*((float64(h)-2*pad)/float64(maxVal-minVal))
		} else {
			y = float64(h) / 2 // flat series
		}
		fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
	}
	return strings.TrimSpace(b.String())
}

// monthFlowRow is the template-facing month bar.
type monthFlowRow struct {
	Label        string // "Aug"
	Month        string // "2026-08"
	IncomeMinor  int64
	ExpenseMinor int64
	IncomePct    int
	ExpensePct   int
}

// fillMonthFlows pads the aggregated rows into a contiguous `months`-long
// series ending at the current month, computing bar heights vs the max.
func fillMonthFlows(rows []store.MonthFlow, months int, now time.Time) []monthFlowRow {
	byMonth := make(map[string]store.MonthFlow, len(rows))
	for _, r := range rows {
		byMonth[r.Month] = r
	}
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(months - 1), 0)
	out := make([]monthFlowRow, months)
	var maxMinor int64
	for i := range out {
		m := start.AddDate(0, i, 0)
		key := m.Format("2006-01")
		f := byMonth[key]
		out[i] = monthFlowRow{Label: m.Format("Jan")[:3], Month: key, IncomeMinor: f.IncomeMinor, ExpenseMinor: f.ExpenseMinor}
		if f.IncomeMinor > maxMinor {
			maxMinor = f.IncomeMinor
		}
		if f.ExpenseMinor > maxMinor {
			maxMinor = f.ExpenseMinor
		}
	}
	if maxMinor > 0 {
		for i := range out {
			out[i].IncomePct = int(out[i].IncomeMinor * 100 / maxMinor)
			out[i].ExpensePct = int(out[i].ExpenseMinor * 100 / maxMinor)
		}
	}
	return out
}

// catSpendRow is the template-facing category bar.
type catSpendRow struct {
	Name       string
	TotalMinor int64
	Pct        int
}

// fillCatSpend computes bar widths against the top spending category.
func fillCatSpend(rows []store.CategorySpend) []catSpendRow {
	out := make([]catSpendRow, 0, len(rows))
	if len(rows) == 0 {
		return out
	}
	top := rows[0].TotalMinor
	if top <= 0 {
		return out
	}
	for _, r := range rows {
		out = append(out, catSpendRow{Name: r.Name, TotalMinor: r.TotalMinor, Pct: int(r.TotalMinor * 100 / top)})
	}
	return out
}
