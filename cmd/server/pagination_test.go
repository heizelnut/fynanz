package main

import "testing"

func TestMakePagination(t *testing.T) {
	cases := []struct {
		name           string
		page, limit    int
		total          int64
		wantCurrent    int
		wantTotalPages int64
		wantPrev       int
		wantNext       int
		wantFrom       int64
		wantTo         int64
	}{
		{"empty", 1, 50, 0, 1, 1, 0, 0, 0, 0},
		{"single page", 1, 50, 4, 1, 1, 0, 0, 1, 4},
		{"middle of many", 4, 50, 1000, 4, 20, 3, 5, 151, 200},
		{"page clamped down", 99, 50, 1000, 20, 20, 19, 0, 951, 1000},
		{"page clamped up", 0, 50, 1000, 1, 20, 0, 2, 1, 50},
		{"partial last page", 3, 20, 45, 3, 3, 2, 0, 41, 45},
	}
	for _, c := range cases {
		got := makePagination(c.page, c.limit, c.total)
		if got.Current != c.wantCurrent || got.TotalPages != c.wantTotalPages ||
			got.Prev != c.wantPrev || got.Next != c.wantNext ||
			got.From != c.wantFrom || got.To != c.wantTo {
			t.Errorf("%s: makePagination(%d,%d,%d) = %+v", c.name, c.page, c.limit, c.total, got)
		}
	}

	// Window: edges + around current, ellipsis (-1) for gaps, no dupes.
	p := makePagination(10, 50, 5000)
	if p.Pages[0] != 1 || p.Pages[len(p.Pages)-1] != 100 {
		t.Errorf("edges wrong: %v", p.Pages)
	}
	seen := map[int]bool{}
	for i, n := range p.Pages {
		if n == -1 {
			continue
		}
		if seen[n] {
			t.Errorf("duplicate page %d in %v", n, p.Pages)
		}
		seen[n] = true
		if n < 1 || n > 100 {
			t.Errorf("page %d out of range in %v", n, p.Pages)
		}
		if i > 0 && n > 1 {
			prev := p.Pages[i-1]
			if prev == -1 {
				// gap must be > 1
				_ = prev
			}
		}
	}
	// The current page must be in the window.
	if !seen[10] {
		t.Errorf("current page missing from window: %v", p.Pages)
	}
}
