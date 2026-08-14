package metrics

import "github.com/prometheus/client_golang/prometheus"

// Recorder is a small helper so the sync layer stays decoupled from
// prometheus details.
type Recorder struct{}

func (Recorder) RunSucceeded(bank string) {
	SyncRuns.With(prometheus.Labels{"bank": bank, "result": "success"}).Inc()
}

func (Recorder) RunFailed(bank string) {
	SyncRuns.With(prometheus.Labels{"bank": bank, "result": "error"}).Inc()
}

func (Recorder) ReauthNeeded(bank string) {
	SyncRuns.With(prometheus.Labels{"bank": bank, "result": "reauth_needed"}).Inc()
}

func (Recorder) Ingested(bank string, n int64) {
	if n > 0 {
		TransactionsIngested.With(prometheus.Labels{"bank": bank}).Add(float64(n))
	}
}

func (Recorder) SetAccounts(bank string, n int) {
	AccountsSynced.With(prometheus.Labels{"bank": bank}).Set(float64(n))
}
