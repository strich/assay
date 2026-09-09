package config

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VerifyModelCredential checks the key for the provider a model names, before
// any expensive work begins. Presence is not enough: a key that is set but
// revoked or mistyped fails exactly like a missing one, only later and more
// expensively.
//
// OpenRouter exposes a free key-introspection endpoint, so its key is checked
// live. Providers without an equivalent cheap endpoint are checked for
// presence only, and the reason is reported rather than hidden.
func VerifyModelCredential(ctx context.Context, model string) error {
	provider, envVar := providerCredential(model)
	if envVar == "" {
		return fmt.Errorf("cannot determine the credential for model %q: expected a provider prefix such as openrouter/", model)
	}
	key := Credential(envVar)
	if key == "" {
		return fmt.Errorf("%s is not set (model %q uses provider %q)", envVar, model, provider)
	}
	if provider != "openrouter" {
		return nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://openrouter.ai/api/v1/key", nil)
	if err != nil {
		return fmt.Errorf("openrouter auth check: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("openrouter auth check failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("openrouter rejected %s (%d): %s", envVar, resp.StatusCode, strings.TrimSpace(string(body)))
	default:
		if resp.StatusCode >= 400 {
			return fmt.Errorf("openrouter auth check failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil
	}
}
