package llm

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Budgets are the cost/abuse caps enforced from the shared ledger (so they hold
// across all pods). A zero value means "no cap" for that dimension.
type Budgets struct {
	RPMPerViewer int   // requests / minute per viewer
	TPHPerViewer int64 // tokens / hour per viewer
	TPHPerApp    int64 // tokens / hour per app
	TPDPerOrg    int64 // tokens / day, org-wide
}

// PGStore is the Postgres-backed usage ledger + budget gate. It is the source of
// truth for budgets — counting from the shared table is what makes the caps
// correct across the 2–6 prod pods.
type PGStore struct {
	pool *pgxpool.Pool
	b    Budgets
}

func NewPGStore(pool *pgxpool.Pool, b Budgets) *PGStore { return &PGStore{pool: pool, b: b} }

// OverBudget reports whether (viewer, appID) has exceeded any configured cap,
// with a suggested retry-after (seconds).
//
// Granularity is one request: this counts only *already-recorded* usage, so the
// request that crosses a cap still runs (and a burst of concurrent requests can
// all pass before any of their rows land). That overage is captured by Record
// and blocks the *next* request, and a single request's spend is bounded by the
// 10k-output cap. These are coarse cost/abuse guards, not hard quotas; exact
// pre-charge accounting (token estimation + reservation) is deliberately out of
// scope. A caller who returns an error here makes Complete fail closed.
func (s *PGStore) OverBudget(ctx context.Context, viewer, appID string) (bool, int, error) {
	if s.b.RPMPerViewer > 0 {
		var n int
		if err := s.pool.QueryRow(ctx,
			`SELECT count(*) FROM llm_usage WHERE viewer_email=$1 AND created_at > now() - interval '1 minute'`,
			viewer).Scan(&n); err != nil {
			return false, 0, err
		}
		if n >= s.b.RPMPerViewer {
			return true, 60, nil
		}
	}
	if s.b.TPHPerViewer > 0 {
		if over, err := s.tokensOver(ctx,
			`SELECT coalesce(sum(input_tokens+output_tokens),0) FROM llm_usage WHERE viewer_email=$1 AND created_at > now() - interval '1 hour'`,
			s.b.TPHPerViewer, viewer); err != nil || over {
			return over, 600, err
		}
	}
	if s.b.TPHPerApp > 0 {
		if over, err := s.tokensOver(ctx,
			`SELECT coalesce(sum(input_tokens+output_tokens),0) FROM llm_usage WHERE app_id=$1 AND created_at > now() - interval '1 hour'`,
			s.b.TPHPerApp, appID); err != nil || over {
			return over, 600, err
		}
	}
	if s.b.TPDPerOrg > 0 {
		if over, err := s.tokensOver(ctx,
			`SELECT coalesce(sum(input_tokens+output_tokens),0) FROM llm_usage WHERE created_at > now() - interval '1 day'`,
			s.b.TPDPerOrg); err != nil || over {
			return over, 3600, err
		}
	}
	return false, 0, nil
}

func (s *PGStore) tokensOver(ctx context.Context, q string, limit int64, args ...any) (bool, error) {
	var tok int64
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&tok); err != nil {
		return false, err
	}
	return tok >= limit, nil
}

// Record appends one usage row (best-effort; failures don't block the response).
func (s *PGStore) Record(ctx context.Context, u Usage) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO llm_usage (viewer_email, app_id, model, input_tokens, output_tokens, cost_micros, request_id, ok)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		u.Viewer, u.AppID, u.Model, u.InputTokens, u.OutputTokens,
		costMicros(u.Model, u.InputTokens, u.OutputTokens), u.RequestID, u.OK)
	return err
}

// price is $/MTok {input, output} per canonical model; cost_micros = millionths
// of a dollar = input_tokens*inPrice + output_tokens*outPrice.
var price = map[string][2]int64{
	"claude-opus-4-8":   {5, 25},
	"claude-sonnet-4-6": {3, 15},
	"claude-haiku-4-5":  {1, 5},
}

func costMicros(model string, in, out int64) int64 {
	p, ok := price[model]
	if !ok {
		p = [2]int64{3, 15} // unknown → sonnet rate
	}
	return in*p[0] + out*p[1]
}
