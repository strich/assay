package harnessx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"time"
)

// TimeoutError reports that a subprocess exceeded its explicit timeout.
type TimeoutError struct {
	Command string
	Timeout time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s timed out after %s", e.Command, e.Timeout)
}

// execWithTimeout runs one subprocess with a hard timeout, returning stdout and
// stderr verbatim. A timeout is reported as *TimeoutError; a missing binary as
// the underlying exec error (isExecNotFound classifies it).
func execWithTimeout(ctx context.Context, bin string, args []string, env []string, dir string, stdin []byte, timeout time.Duration) (string, string, int, error) {
	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	// Bound the wait for pipe closure after a kill: a descendant that inherits
	// stdout/stderr must not keep the runner waiting past its timeout.
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), -1, &TimeoutError{Command: bin, Timeout: timeout}
	}
	if runCtx.Err() == context.Canceled {
		return stdout.String(), stderr.String(), -1, runCtx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), stderr.String(), exitErr.ExitCode(), nil
		}
		return stdout.String(), stderr.String(), -1, err
	}
	return stdout.String(), stderr.String(), 0, nil
}

// ansiRe matches ANSI escape sequences emitted by CLI tooling.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// stripANSI removes terminal color/control sequences from a captured stream.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// isExecNotFound reports whether err is a binary-not-found failure.
func isExecNotFound(err error) bool {
	if err == nil {
		return false
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, exec.ErrNotFound)
	}
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}
