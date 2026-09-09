package config

import "testing"

// Credentials must reach providers without surrounding whitespace. A production
// run failed repeatedly with OpenRouter answering
//
//	401 {"error": {"message": "User not found.", "code": 401}}
//
// for a key the operator had validated by hand: the secret carried a trailing
// newline, which is forwarded byte for byte, so the provider saw an
// Authorization header it could not match. That 401 is identical to an unknown
// key's, which is why the symptom pointed at the key rather than its transport.

func TestCredentialTrimsSurroundingWhitespace(t *testing.T) {
	const clean = "sk-or-v1-abc123"
	for name, raw := range map[string]string{
		"newline":         "sk-or-v1-abc123\n",
		"crlf":            "sk-or-v1-abc123\r\n",
		"spaces":          " sk-or-v1-abc123 ",
		"tabs":            "\tsk-or-v1-abc123\t",
		"leading-newline": "\nsk-or-v1-abc123",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENROUTER_API_KEY", raw)
			if got := Credential("OPENROUTER_API_KEY"); got != clean {
				t.Errorf("Credential() = %q, want %q", got, clean)
			}
		})
	}
}

func TestCredentialLeavesInteriorAlone(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "  sk-or-v1-a b c  ")
	if got, want := Credential("OPENROUTER_API_KEY"), "sk-or-v1-a b c"; got != want {
		t.Errorf("Credential() = %q, want %q", got, want)
	}
}

func TestCredentialUnsetIsEmpty(t *testing.T) {
	if got := Credential("ASSAY_DEFINITELY_UNSET_CREDENTIAL"); got != "" {
		t.Errorf("Credential() = %q, want empty", got)
	}
}

// A key of only whitespace must read as absent. ProviderEnv drops empty
// values, so this is what stops a whitespace-only secret being forwarded as a
// present-but-unusable credential.
func TestCredentialWhitespaceOnlyIsAbsent(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "   \n")
	if got := Credential("OPENROUTER_API_KEY"); got != "" {
		t.Errorf("Credential() = %q, want empty", got)
	}
}

// The seam that fed the harness its 401: the opencode/aforge subprocess env.
func TestProviderEnvTrimsCredentials(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-abc123\n")
	t.Setenv("GH_TOKEN", "ghs_token\n")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	env := AIIntegrationConfig{}.ProviderEnv()

	if got, want := env["OPENROUTER_API_KEY"], "sk-or-v1-abc123"; got != want {
		t.Errorf("OPENROUTER_API_KEY = %q, want %q", got, want)
	}
	if got, want := env["GH_TOKEN"], "ghs_token"; got != want {
		t.Errorf("GH_TOKEN = %q, want %q", got, want)
	}
}

func TestProviderEnvOmitsWhitespaceOnlyCredential(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "  \n  ")
	env := AIIntegrationConfig{}.ProviderEnv()
	if v, ok := env["OPENROUTER_API_KEY"]; ok {
		t.Errorf("whitespace-only key was forwarded as %q; want it omitted", v)
	}
}
