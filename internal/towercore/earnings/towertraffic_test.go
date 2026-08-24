package earnings

// towertraffic_test.go covers the per-Tower, per-model traffic rollup the admin detail view
// reads. It runs through BOTH ledgers (mem + Postgres when a DSN is set) so the two
// deployments answer identically, and it asserts the money invariants that make the view
// safe to show: self-dealing is surfaced but never earns, corroborated/uncorroborated split
// the attempts, and the window filter matches OwedTo's.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func trafAccrual(a Accrual) Accrual {
	if a.AttemptID == "" || a.TowerID == "" {
		panic("test accrual needs ids")
	}
	return a
}

func TestTowerTraffic(t *testing.T) {
	now := time.Now().UTC()
	for name, st := range stores(t) {
		st := st
		t.Run(name, func(t *testing.T) {
			// Two models on tw-A, one on tw-B, plus a self-dealing row and an uncorroborated row.
			rows := []Accrual{
				{TowerID: "tw-A", Owner: "op", AttemptID: "a1", Model: "llama", UsageIn: 100, UsageOut: 200, Micros: 500, Corroborated: true, At: now},
				{TowerID: "tw-A", Owner: "op", AttemptID: "a2", Model: "llama", UsageIn: 50, UsageOut: 80, Micros: 300, Corroborated: false, At: now},
				{TowerID: "tw-A", Owner: "op", AttemptID: "a3", Model: "qwen", UsageIn: 10, UsageOut: 20, Micros: 90, Corroborated: true, At: now},
				{TowerID: "tw-A", Owner: "op", AttemptID: "a4", Model: "llama", UsageIn: 1, UsageOut: 1, Micros: 999, SelfDealing: true, Corroborated: true, At: now},
				{TowerID: "tw-B", Owner: "op2", AttemptID: "b1", Model: "llama", UsageIn: 5, UsageOut: 5, Micros: 5, Corroborated: true, At: now},
			}
			for _, r := range rows {
				require.NoError(t, st.Accrue(trafAccrual(r)))
			}

			tt, err := st.TowerTraffic("tw-A", time.Time{})
			require.NoError(t, err)
			require.Equal(t, "tw-A", tt.TowerID)
			require.Len(t, tt.Models, 2, "only tw-A's models, sorted")
			require.Equal(t, "llama", tt.Models[0].Model, "sorted by model id")
			require.Equal(t, "qwen", tt.Models[1].Model)

			llama := tt.Models[0]
			require.Equal(t, 3, llama.Attempts, "3 llama attempts incl the self-dealing one")
			require.Equal(t, 2, llama.Corroborated, "a1 and a4 corroborated")
			require.Equal(t, 1, llama.Uncorroborated, "a2 uncorroborated")
			require.Equal(t, int64(151), llama.UsageIn, "100+50+1")
			require.Equal(t, int64(281), llama.UsageOut, "200+80+1")
			require.Equal(t, int64(800), llama.Micros, "500+300, NOT the 999 self-dealing")
			require.Equal(t, int64(999), llama.SelfDealt, "self-dealing surfaced separately")

			qwen := tt.Models[1]
			require.Equal(t, 1, qwen.Attempts)
			require.Equal(t, int64(90), qwen.Micros)

			// Totals across models, self-dealing excluded from Micros.
			require.Equal(t, 4, tt.Attempts, "all tw-A rows")
			require.Equal(t, int64(890), tt.Micros, "800 llama + 90 qwen")
			require.Equal(t, int64(999), tt.SelfDealt)

			// tw-B is isolated.
			ttB, err := st.TowerTraffic("tw-B", time.Time{})
			require.NoError(t, err)
			require.Len(t, ttB.Models, 1)
			require.Equal(t, int64(5), ttB.Micros)

			// An unknown Tower is empty, not an error.
			ttNone, err := st.TowerTraffic("tw-nope", time.Time{})
			require.NoError(t, err)
			require.Empty(t, ttNone.Models)
			require.Zero(t, ttNone.Attempts)
		})
	}
}

func TestTowerTrafficWindowFilters(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	for name, st := range stores(t) {
		st := st
		t.Run(name, func(t *testing.T) {
			require.NoError(t, st.Accrue(Accrual{TowerID: "tw-W", Owner: "o", AttemptID: "old", Model: "m", Micros: 100, At: old}))
			require.NoError(t, st.Accrue(Accrual{TowerID: "tw-W", Owner: "o", AttemptID: "new", Model: "m", Micros: 200, At: now}))

			all, err := st.TowerTraffic("tw-W", time.Time{})
			require.NoError(t, err)
			require.Equal(t, int64(300), all.Micros, "zero since is all-time")

			recent, err := st.TowerTraffic("tw-W", now.Add(-time.Hour))
			require.NoError(t, err)
			require.Equal(t, int64(200), recent.Micros, "window excludes the old row")
			require.Equal(t, 1, recent.Attempts)
		})
	}
}
