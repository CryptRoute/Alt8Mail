package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var errAIBudget = errors.New("AI monthly budget is exhausted")

type aiUsageSnapshot struct {
	Month       string
	Requests    int64
	InputChars  int64
	OutputChars int64
	CostMicros  int64
	BudgetCents int64
}

func (s *Server) aiUsage(ctx context.Context, tenantID string) (aiUsageSnapshot, error) {
	snapshot := aiUsageSnapshot{Month: time.Now().UTC().Format("2006-01")}
	_ = s.store.DB.QueryRow(ctx, `SELECT COALESCE(monthly_budget_cents,0) FROM public.tenant_ai_settings WHERE tenant_id=$1`, tenantID).Scan(&snapshot.BudgetCents)
	err := s.store.DB.QueryRow(ctx, `
		SELECT requests, input_chars, output_chars, cost_micros FROM public.ai_usage
		WHERE tenant_id=$1 AND month=date_trunc('month', now())::date
	`, tenantID).Scan(&snapshot.Requests, &snapshot.InputChars, &snapshot.OutputChars, &snapshot.CostMicros)
	if err != nil {
		return snapshot, nil
	}
	return snapshot, nil
}

// aiBudgetCheck rejects AI calls once the tenant's monthly estimate reaches
// its configured cap. A zero budget means uncapped.
func (s *Server) aiBudgetCheck(ctx context.Context, tenantID string) error {
	var budgetCents, costMicros int64
	_ = s.store.DB.QueryRow(ctx, `SELECT COALESCE(monthly_budget_cents,0) FROM public.tenant_ai_settings WHERE tenant_id=$1`, tenantID).Scan(&budgetCents)
	_ = s.store.DB.QueryRow(ctx, `
		SELECT cost_micros FROM public.ai_usage
		WHERE tenant_id=$1 AND month=date_trunc('month', now())::date
	`, tenantID).Scan(&costMicros)
	if budgetCents > 0 && costMicros >= budgetCents*10000 {
		return errAIBudget
	}
	return nil
}

// recordAIUsage keeps a deterministic conservative estimate: 1 micro-credit
// per input character and 4 per output character. Providers' exact billing
// stays external; the tenant cap is enforced against this estimate.
func (s *Server) recordAIUsage(ctx context.Context, tenantID string, inputChars, outputChars int) {
	_, _ = s.store.DB.Exec(ctx, `
		INSERT INTO public.ai_usage (tenant_id, month, requests, input_chars, output_chars, cost_micros)
		VALUES ($1, date_trunc('month', now())::date, 1, $2, $3, $4)
		ON CONFLICT (tenant_id, month) DO UPDATE SET
		  requests = public.ai_usage.requests + 1,
		  input_chars = public.ai_usage.input_chars + EXCLUDED.input_chars,
		  output_chars = public.ai_usage.output_chars + EXCLUDED.output_chars,
		  cost_micros = public.ai_usage.cost_micros + EXCLUDED.cost_micros
	`, tenantID, int64(inputChars), int64(outputChars), int64(inputChars)+int64(outputChars)*4)
}

func (s *Server) callAIForTenant(ctx context.Context, tenantID string, settings aiSettings, system, user string, maxTokens int, temperature float64) (string, error) {
	if settings.TenantID == "" {
		settings.TenantID = tenantID
	}
	return s.callAI(ctx, settings, system, user, maxTokens, temperature)
}

func (s *Server) aiUsageHandler(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	usage, err := s.aiUsage(r.Context(), sess.User.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AI_USAGE", "Could not load AI usage")
		return
	}
	remaining := int64(-1)
	if usage.BudgetCents > 0 {
		remaining = max(int64(0), usage.BudgetCents*10000-usage.CostMicros)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"month": usage.Month, "requests": usage.Requests, "inputChars": usage.InputChars,
		"outputChars": usage.OutputChars, "costMicros": usage.CostMicros,
		"budgetCents": usage.BudgetCents, "remainingMicros": remaining,
		"budgetExhausted": usage.BudgetCents > 0 && usage.CostMicros >= usage.BudgetCents*10000,
	})
}

func (s *Server) registerAIGovernanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ai/usage", s.requireUser(s.aiUsageHandler))
}

func aiBudgetError(err error) bool { return errors.Is(err, errAIBudget) }

func aiBudgetMessage(err error) string {
	if aiBudgetError(err) {
		return "The organization's monthly AI budget is exhausted. An administrator can raise it in System Control."
	}
	return fmt.Sprint(err)
}
