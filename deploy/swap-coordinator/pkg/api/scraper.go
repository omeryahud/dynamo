package api

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/ai-dynamo/dynamo/swap-coordinator/pkg/state"
)

var scraperLog = ctrl.Log.WithName("ttft-scraper")

const ttftMetricPrefix = "dynamo_frontend_time_to_first_token_seconds"

// StartScraper runs the TTFT metrics scraper in a loop until ctx is cancelled.
// It scrapes all registered frontend pods every interval.
func StartScraper(ctx context.Context, stateManager *state.Manager, interval time.Duration) {
	scraperLog.Info("Starting TTFT scraper", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	client := &http.Client{Timeout: 5 * time.Second}

	for {
		select {
		case <-ctx.Done():
			scraperLog.Info("TTFT scraper stopped")
			return
		case <-ticker.C:
			scrapeFrontends(ctx, stateManager, client)
		}
	}
}

func scrapeFrontends(ctx context.Context, stateManager *state.Manager, client *http.Client) {
	frontends := stateManager.ListFrontends()
	if len(frontends) == 0 {
		return
	}

	// Aggregate deltas per DGD
	type dgdDelta struct {
		sumDelta   float64
		countDelta uint64
	}
	dgdDeltas := make(map[string]*dgdDelta)

	for _, fe := range frontends {
		sum, count, err := scrapeMetrics(ctx, client, fe.PodIP, fe.Port)
		if err != nil {
			scraperLog.V(1).Info("Failed to scrape frontend",
				"pod", fe.PodName, "ip", fe.PodIP, "error", err)
			continue
		}

		prevSum, prevCount, hasPrev := stateManager.GetLastScrape(fe.PodName)
		stateManager.SetLastScrape(fe.PodName, sum, count)

		if !hasPrev {
			continue // Need two data points to compute a delta
		}

		deltaSum := sum - prevSum
		deltaCount := count - prevCount
		if deltaCount == 0 || deltaSum <= 0 {
			continue // No new requests in this interval
		}

		dgdKey := fe.Namespace + "/" + fe.DGDName
		d, ok := dgdDeltas[dgdKey]
		if !ok {
			d = &dgdDelta{}
			dgdDeltas[dgdKey] = d
		}
		d.sumDelta += deltaSum
		d.countDelta += deltaCount
	}

	// Record one sample per DGD
	for dgdKey, d := range dgdDeltas {
		if d.countDelta == 0 {
			continue
		}
		avgMS := (d.sumDelta / float64(d.countDelta)) * 1000.0 // seconds -> ms
		stateManager.RecordTTFTSample(dgdKey, avgMS)
		scraperLog.V(1).Info("Recorded TTFT sample",
			"dgd", dgdKey, "avgMS", fmt.Sprintf("%.2f", avgMS),
			"deltaCount", d.countDelta)
	}
}

// scrapeMetrics fetches the /metrics endpoint and extracts the TTFT histogram sum and count.
func scrapeMetrics(ctx context.Context, client *http.Client, podIP string, port int) (float64, uint64, error) {
	url := fmt.Sprintf("http://%s:%d/metrics", podIP, port)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var sum float64
	var count uint64
	foundSum := false
	foundCount := false

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}

		if !foundSum && strings.HasPrefix(line, ttftMetricPrefix+"_sum") {
			val := extractMetricValue(line)
			if val != "" {
				if parsed, err := strconv.ParseFloat(val, 64); err == nil {
					sum = parsed
					foundSum = true
				}
			}
		}

		if !foundCount && strings.HasPrefix(line, ttftMetricPrefix+"_count") {
			val := extractMetricValue(line)
			if val != "" {
				if parsed, err := strconv.ParseUint(val, 10, 64); err == nil {
					count = parsed
					foundCount = true
				}
			}
		}

		if foundSum && foundCount {
			break
		}
	}

	if !foundSum || !foundCount {
		return 0, 0, fmt.Errorf("TTFT metrics not found in response")
	}

	return sum, count, nil
}

// extractMetricValue extracts the numeric value from a Prometheus metrics line.
// Format: metric_name{labels} value  or  metric_name value
func extractMetricValue(line string) string {
	// Find the value after the last space
	// Handle lines like: metric_name{label="val"} 123.45
	// or: metric_name 123.45
	idx := strings.LastIndex(line, " ")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(line[idx+1:])
}
