package cmd

import (
	"strings"
	"testing"
)

func TestInjectContainerEnvDoesNotExposeValuesInArgs(t *testing.T) {
	args := injectContainerEnv([]string{"run", "--rm", "example/app:latest"}, map[string]string{
		"API_KEY": "secret-value",
		"PORT":    "8080",
	})

	joined := strings.Join(args, " ")
	if strings.Contains(joined, "secret-value") || strings.Contains(joined, "8080") {
		t.Fatalf("container args expose secret values: %q", joined)
	}
	if !strings.Contains(joined, "--env API_KEY") || !strings.Contains(joined, "--env PORT") {
		t.Fatalf("container args do not pass environment names: %q", joined)
	}
}
