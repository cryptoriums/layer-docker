// Package plan reads the chain's scheduled upgrade so the matching binary can be
// staged before the node reaches the upgrade height.
//
// This is what makes upgrades hands-off: governance passes a plan, the watcher
// sees it, downloads that release, and cosmovisor swaps binaries on its own. No
// operator action between the proposal passing and the upgrade landing.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Plan is a scheduled chain upgrade.
type Plan struct {
	Name   string // upgrade name, which for Tellor matches the release tag
	Height int64  // block height at which the chain halts for the upgrade
}

// Client queries a chain REST endpoint for the current upgrade plan.
type Client struct {
	urls []string
	http *http.Client
}

// New returns a Client that tries each REST base URL in order.
//
// Multiple endpoints matter: whether an upgrade is scheduled is public data, and
// asking only our own node means going blind exactly when that node is down or
// mid-restart — which is when an upgrade is most likely to be missed.
func New(urls []string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{urls: urls, http: &http.Client{Timeout: timeout}}
}

// currentPlanResponse mirrors the subset of the upgrade module's response we use.
// The plan is null when no upgrade is scheduled.
type currentPlanResponse struct {
	Plan *struct {
		Name   string `json:"name"`
		Height string `json:"height"`
	} `json:"plan"`
}

// Current returns the scheduled upgrade, or nil when none is scheduled.
func (c *Client) Current(ctx context.Context) (*Plan, error) {
	var lastErr error
	for _, base := range c.urls {
		p, err := c.fetch(ctx, strings.TrimRight(base, "/")+"/cosmos/upgrade/v1beta1/current_plan")
		if err == nil {
			return p, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no chain endpoints configured")
	}
	return nil, lastErr
}

func (c *Client) fetch(ctx context.Context, url string) (*Plan, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var parsed currentPlanResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	if parsed.Plan == nil || parsed.Plan.Name == "" {
		return nil, nil
	}

	height, err := strconv.ParseInt(parsed.Plan.Height, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse plan height %q: %w", parsed.Plan.Height, err)
	}
	return &Plan{Name: parsed.Plan.Name, Height: height}, nil
}
