package main

import (
	"fmt"
	"html/template"
	"net/url"
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

// svgPoint is one vertex in chart viewBox space (scaled).
type svgPoint struct{ X, Y float64 }

// lineSeries is one balance series for the SVG: a single polyline spanning
// every snapshot in the fixed 1200x200 viewBox (the line interpolates
// straight between points; there is no gap-breaking or dot-marker logic —
// the chart stays a simple line).
type lineSeries struct {
	Label  string
	Points string // "x,y x,y …" polyline points attr (every point)
	Class  string // bal-0, bal-1… (stroke color in CSS)
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

const (
	balW, balH = 1200.0, 200.0 // viewBox of the balance svg
	balPad     = 10.0
)

// svgCoord scales one point of a series into viewBox space: x spread by
// index (shared across series so they're comparable at the same column), y
// by the global min/max with 10px margins.
func svgCoord(i, n int, minor, minVal, maxVal int64) svgPoint {
	x := balPad + float64(i)*((balW-2*balPad)/float64(max(1, n-1)))
	var y float64
	if maxVal > minVal {
		y = balPad + (float64(maxVal)-float64(minor))*((balH-2*balPad)/float64(maxVal-minVal))
	} else {
		y = balH / 2 // flat series
	}
	return svgPoint{x, y}
}

// polylineAttr joins scaled coordinates into a polyline points attribute.
func polylineAttr(coords []svgPoint) string {
	var b strings.Builder
	for _, c := range coords {
		fmt.Fprintf(&b, "%.1f,%.1f ", c.X, c.Y)
	}
	return strings.TrimSpace(b.String())
}

// monthFlowRow is the template-facing month bar. From/ToDate and TxURL let
// the bar itself be a link to that month's transactions (date-filtered).
type monthFlowRow struct {
	Label        string // "Aug"
	Month        string // "2026-08"
	IncomeMinor  int64
	ExpenseMinor int64
	IncomePct    int
	ExpensePct   int
	FromDate     string       // first day of the month ("2026-08-01")
	ToDate       string       // last day of the month
	TxURL        template.URL // /transactions?date_from=…&date_to=…
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
		from := key + "-01"
		to := m.AddDate(0, 1, 0).AddDate(0, 0, -1).Format("2006-01-02")
		q := url.Values{}
		q.Set("date_from", from)
		q.Set("date_to", to)
		out[i] = monthFlowRow{
			Label: m.Format("Jan")[:3], Month: key,
			IncomeMinor: f.IncomeMinor, ExpenseMinor: f.ExpenseMinor,
			FromDate: from, ToDate: to,
			TxURL: template.URL("/transactions?" + q.Encode()),
		}
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

// flowDonut drives the income-vs-expense donut (AGENTS.md §4): two arcs, the
// income segment green and the expense segment red, lengths proportional to
// their share of the window's total movement; the center shows the net.
type flowDonut struct {
	IncomeMinor  int64
	ExpenseMinor int64
	NetMinor     int64 // income - expense (signed)
	TotalMinor   int64 // income + expense (arc denominator)
	IncomePct    int   // percent of total (0..100)
	ExpensePct   int   // 100 - IncomePct (so arcs always tile exactly)
	HasData      bool
}

// computeFlowDonut totals the window's income/expense rows into donut arcs.
// Percentages are integer and derived from income only (expense = the
// remainder) so the two arcs always tile the full circle with no rounding gap.
func computeFlowDonut(rows []monthFlowRow) flowDonut {
	var d flowDonut
	for _, r := range rows {
		d.IncomeMinor += r.IncomeMinor
		d.ExpenseMinor += r.ExpenseMinor
	}
	d.TotalMinor = d.IncomeMinor + d.ExpenseMinor
	d.NetMinor = d.IncomeMinor - d.ExpenseMinor
	if d.TotalMinor > 0 {
		d.HasData = true
		d.IncomePct = int(d.IncomeMinor * 100 / d.TotalMinor)
		d.ExpensePct = 100 - d.IncomePct
	}
	return d
}

// catSpendRow is the template-facing category bar.
type catSpendRow struct {
	Name       string
	TotalMinor int64
	Pct        int
	IsTransfer bool // internal transfers render grayed (not real spending)
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
		out = append(out, catSpendRow{Name: r.Name, TotalMinor: r.TotalMinor, Pct: int(r.TotalMinor * 100 / top), IsTransfer: r.IsTransfer})
	}
	return out
}
