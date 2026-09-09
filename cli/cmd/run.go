package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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

			if verified, vErr := verifySignature(result.EncryptedData, result.Signature, result.PushedByPubKey); vErr != nil {
				return fmt.Errorf("signature verification failed: %w\nRefusing to run with unverified secrets", vErr)
			} else if !verified {
				return fmt.Errorf("secrets are not signed — this is a legacy secret pushed before signature verification was enabled\nFor security, re-push secrets using a recent version of dotsync: dotsync push --env %s", env)
			} else {
				fmt.Fprintln(os.Stderr, ok(fmt.Sprintf("Signature verified (%s, ed25519)", result.PushedBy)))
			}

			password, err := resolvePassword(client, projCfg.ProjectSlug)
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

			// For docker/podman, when the subcommand is "run", automatically
			// inject --env KEY=VALUE for every secret right after "run".
			// This is the only way to get secrets into a container without
			// writing anything to disk — the parent process has the secrets
			// in its environment; --env KEY passes them into the container.
			if isContainerRuntime(command) {
				cmdArgs = injectContainerEnv(cmdArgs, secrets)
			}

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

			fmt.Fprintln(os.Stderr, info(fmt.Sprintf("Injecting %d secrets (%s/%s v%d)",
				len(secrets), projCfg.ProjectSlug, env, result.Version)))

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

// isContainerRuntime returns true when the command is docker or podman.

// isContainerRuntime returns true when the command is docker, podman, or compatible runtimes.
func isContainerRuntime(command string) bool {
	base := command
	for i := len(command) - 1; i >= 0; i-- {
		if command[i] == '/' {
			base = command[i+1:]
			break
		}
	}
	switch base {
	case "docker", "podman", "nerdctl", "lima":
		return true
	}
	return false
}

// injectContainerEnv inserts --env KEY=VALUE flags into container run args.
//
// Only activates when the subcommand is "run". Uses a two-pass approach that
// is resilient to new Docker/Podman flags — it never needs a maintained list.
//
// Strategy:
//   - "--flag=value" syntax: consume no extra arg (safe to skip)
//   - Known short flags (-e, -v, -p, -l, -m, -u, -w, -h): consume next arg
//   - Single-char short flags (-d, -i, -t): boolean, no value
//   - Unknown long flags (--foo): if next arg doesn't look like an image name,
//     treat it as the flag's value. This is the conservative fallback.
//
// Before: [run, -d, --pull=never, myimage]
// After:  [run, -d, --pull=never, --env KEY=VAL, ..., myimage]
func injectContainerEnv(args []string, secrets map[string]string) []string {
	if len(args) == 0 || args[0] != "run" {
		return args
	}

	shortValueFlags := map[string]bool{
		"-e": true, "-v": true, "-p": true, "-l": true,
		"-m": true, "-u": true, "-w": true, "-h": true,
	}

	insertAt := 1
	i := 1
	for i < len(args) {
		arg := args[i]

		if arg == "--" {
			insertAt = i
			break
		}
		if len(arg) == 0 || arg[0] != '-' {
			insertAt = i
			break
		}
		if strings.Contains(arg, "=") {
			i++
			insertAt = i
			continue
		}
		if shortValueFlags[arg] {
			i += 2
			insertAt = i
			continue
		}
		// Single-char short flag: boolean, no value
		if len(arg) == 2 && arg[0] == '-' && arg[1] != '-' {
			i++
			insertAt = i
			continue
		}
		// Long flag without "=": check if next arg is a value
		if len(arg) > 2 && arg[0] == '-' && arg[1] == '-' {
			i++
			insertAt = i
			if i < len(args) {
				next := args[i]
				if len(next) > 0 && next[0] != '-' {
					looksLikeImage := strings.Contains(next, "/") ||
						strings.Contains(next, ":") ||
						next == "latest"
					if !looksLikeImage {
						i++
						insertAt = i
					}
				}
			}
			continue
		}
		i++
		insertAt = i
	}

	envFlags := make([]string, 0, len(secrets)*2)
	for k, v := range secrets {
		envFlags = append(envFlags, "--env", k+"="+v)
	}

	result := make([]string, 0, len(args)+len(envFlags))
	result = append(result, args[:insertAt]...)
	result = append(result, envFlags...)
	result = append(result, args[insertAt:]...)
	return result
}
