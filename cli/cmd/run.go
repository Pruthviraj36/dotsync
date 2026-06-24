package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Pruthviraj36/dotsync/cli/api"
	"github.com/Pruthviraj36/dotsync/cli/config"
	cliCrypto "github.com/Pruthviraj36/dotsync/cli/crypto"
)

func runCmd() *cobra.Command {
	var envFlag string
	var versionFlag int

	cmd := &cobra.Command{
		Use:   "run -- <command> [args...]",
		Short: "Run a command with secrets injected as environment variables",
		Long: `Decrypts your project secrets and injects them directly into the
subprocess environment. Nothing is written to disk — secrets exist only
in memory for the duration of the process.

This is the recommended way to run your application in development:

  dotsync run -- node server.js
  dotsync run -- python manage.py runserver
  dotsync run -- go run ./cmd/server
  dotsync run --env staging -- ./scripts/migrate.sh

Secrets from DotSync are merged with your current shell environment.
DotSync values take precedence over existing env vars with the same name.

The separator '--' is required to distinguish dotsync flags from your
command's flags (e.g. dotsync run -- node --inspect server.js).`,
		Example: `  dotsync run -- node server.js
  dotsync run --env staging -- ./deploy.sh
  dotsync run --version 3 -- node server.js`,
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireLogin()
			if err != nil {
				return err
			}

			projCfg, err := config.LoadProject()
			if err != nil {
				return err
			}

			env := envFlag
			if env == "" {
				env = projCfg.DefaultEnv
			}

			client := api.New(cfg)

			var result *api.PullResponse
			if versionFlag > 0 {
				result, err = client.PullVersion(projCfg.ProjectSlug, env, versionFlag)
			} else {
				result, err = client.Pull(projCfg.ProjectSlug, env)
			}
			if err != nil {
				return fmt.Errorf("fetch secrets: %w", err)
			}

			password, err := config.GetProjectPassword(projCfg.ProjectSlug)
			if err != nil {
				return err
			}

			plaintext, err := cliCrypto.DecryptEnvFile(
				result.EncryptedData, result.Nonce,
				password, projCfg.ProjectSlug,
			)
			if err != nil {
				return fmt.Errorf("decrypt secrets: %w", err)
			}

			// Parse decrypted secrets into key=value pairs
			secrets := cliCrypto.ParseEnvFile(plaintext)

			// Build subprocess environment: start from current shell env,
			// overlay with DotSync secrets. DotSync values win on conflict.
			baseEnv := os.Environ()
			injected := make(map[string]bool)
			for k := range secrets {
				injected[k] = true
			}

			// Filter out any existing values that DotSync will override
			var procEnv []string
			for _, e := range baseEnv {
				key := e
				if idx := len(e); idx > 0 {
					for i, c := range e {
						if c == '=' {
							key = e[:i]
							break
						}
					}
				}
				if !injected[key] {
					procEnv = append(procEnv, e)
				}
			}

			// Inject secrets
			for k, v := range secrets {
				procEnv = append(procEnv, k+"="+v)
			}

			command := args[0]
			cmdArgs := args[1:]

			// Resolve the binary so errors are clear
			bin, err := exec.LookPath(command)
			if err != nil {
				return fmt.Errorf("command not found: %s", command)
			}

			proc := exec.Command(bin, cmdArgs...)
			proc.Env = procEnv
			proc.Stdin = os.Stdin
			proc.Stdout = os.Stdout
			proc.Stderr = os.Stderr

			// Forward signals to the subprocess so Ctrl-C, SIGTERM etc.
			// reach the actual process and it can clean up properly.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			go func() {
				for sig := range sigCh {
					if proc.Process != nil {
						proc.Process.Signal(sig)
					}
				}
			}()

			fmt.Fprintf(os.Stderr, dim("→ dotsync: injecting %d secrets (%s/%s v%d)")+"\n",
				len(secrets), projCfg.ProjectSlug, env, result.Version)

			if err := proc.Run(); err != nil {
				// Propagate the exit code from the subprocess
				if exitErr, ok := err.(*exec.ExitError); ok {
					os.Exit(exitErr.ExitCode())
				}
				return err
			}

			signal.Stop(sigCh)
			close(sigCh)
			return nil
		},
	}

	cmd.Flags().StringVarP(&envFlag, "env", "e", "", "environment (dev|staging|production)")
	cmd.Flags().IntVar(&versionFlag, "version", 0, "use a specific secret version (default: latest)")
	return cmd
}
