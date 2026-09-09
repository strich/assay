package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VerifyToken checks a GitHub token against the API before any expensive work
// begins. A wrong or missing credential must fail in seconds with a message
// naming the cause, not after a multi-minute clone and review.
//
// GET /rate_limit is used because it is free, works for classic and
// fine-grained PATs, and returns 401 for a rejected token.
func VerifyToken(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("GH_TOKEN is not set; posting a review requires it (pass --dry-run to review without posting)")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, defaultBaseURL+"/rate_limit", nil)
	if err != nil {
		return fmt.Errorf("github: build auth check: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("github: auth check failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("github: token rejected (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	case resp.StatusCode >= 400:
		return fmt.Errorf("github: auth check failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
