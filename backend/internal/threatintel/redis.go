package threatintel

import (
	"context"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"
)

const (
	SenderMap = "a8m_reported_senders"
	DomainMap = "a8m_reported_domains"
	URLMap    = "a8m_reported_urls"
)

// ReportIndicators is one confirmed report expressed as the values that may be
// published. URLHosts must already contain only reviewer-approved hosts.
type ReportIndicators struct {
	Sender   string
	Domain   string
	URLHosts []string
}

// IndicatorSet is the complete active set published to Rspamd. It is always
// rebuilt from every still-confirmed report, never patched one indicator at a
// time.
type IndicatorSet struct {
	Senders []string
	Domains []string
	URLs    []string
}

// ActiveSet merges confirmed reports into a deduplicated, sorted set.
func ActiveSet(reports []ReportIndicators) IndicatorSet {
	senders := map[string]struct{}{}
	domains := map[string]struct{}{}
	urls := map[string]struct{}{}
	for _, report := range reports {
		if value := normalizeIndicator(report.Sender); value != "" {
			senders[value] = struct{}{}
		}
		if value := normalizeIndicator(report.Domain); value != "" {
			domains[value] = struct{}{}
		}
		for _, host := range report.URLHosts {
			if value := normalizeIndicator(host); value != "" {
				urls[value] = struct{}{}
			}
		}
	}
	return IndicatorSet{
		Senders: sortedKeys(senders),
		Domains: sortedKeys(domains),
		URLs:    sortedKeys(urls),
	}
}

func normalizeIndicator(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func sortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// Publish atomically replaces all three Redis hashes with the supplied active
// set. Because the replacement is built from the full confirmed set, removing
// one report can never remove an indicator that another confirmed report still
// contributes.
func Publish(ctx context.Context, client redis.UniversalClient, set IndicatorSet) error {
	pipe := client.TxPipeline()
	pipe.Del(ctx, SenderMap, DomainMap, URLMap)
	for _, sender := range set.Senders {
		pipe.HSet(ctx, SenderMap, sender, "1")
	}
	for _, domain := range set.Domains {
		pipe.HSet(ctx, DomainMap, domain, "1")
	}
	for _, host := range set.URLs {
		pipe.HSet(ctx, URLMap, host, "1")
	}
	_, err := pipe.Exec(ctx)
	return err
}

// Clear removes the published maps. It is used only when the operator turns
// the sharing setting off.
func Clear(ctx context.Context, client redis.UniversalClient) error {
	return client.Del(ctx, SenderMap, DomainMap, URLMap).Err()
}
