package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// secretPattern defines a secret pattern to scan for.
type secretPattern struct {
	name    string
	pattern *regexp.Regexp
	// severity: high = should never be committed, medium = likely sensitive
	severity string
}

// These patterns detect real secrets that common services issue.
// They're specific enough to have very low false-positive rates.
var secretPatterns = []secretPattern{
	{
		name:     "AWS Access Key",
		pattern:  regexp.MustCompile(`(?i)AKIA[0-9A-Z]{16}`),
		severity: "high",
	},
	{
		name:     "AWS Secret Key",
		pattern:  regexp.MustCompile(`(?i)aws.{0,20}['\"][0-9a-zA-Z/+]{40}['\"]`),
		severity: "high",
	},
	{
		name:     "GitHub Personal Access Token",
		pattern:  regexp.MustCompile(`ghp_[0-9a-zA-Z]{36}`),
		severity: "high",
	},
	{
		name:     "GitHub OAuth Token",
		pattern:  regexp.MustCompile(`gho_[0-9a-zA-Z]{36}`),
		severity: "high",
	},
	{
		name:     "GitHub Actions Token",
		pattern:  regexp.MustCompile(`ghs_[0-9a-zA-Z]{36}`),
		severity: "high",
	},
	{
		name:     "Stripe Secret Key (live)",
		pattern:  regexp.MustCompile(`sk_live_[0-9a-zA-Z]{24,}`),
		severity: "high",
	},
	{
		name:     "Stripe Secret Key (test)",
		pattern:  regexp.MustCompile(`sk_test_[0-9a-zA-Z]{24,}`),
		severity: "medium",
	},
	{
		name:     "Stripe Restricted Key",
		pattern:  regexp.MustCompile(`rk_live_[0-9a-zA-Z]{24,}`),
		severity: "high",
	},
	{
		name:     "Stripe Webhook Secret",
		pattern:  regexp.MustCompile(`whsec_[0-9a-zA-Z]{32,}`),
		severity: "high",
	},
	{
		name:     "Slack Bot Token",
		pattern:  regexp.MustCompile(`xoxb-[0-9]{11}-[0-9]{11}-[0-9a-zA-Z]{24}`),
		severity: "high",
	},
	{
		name:     "Slack User Token",
		pattern:  regexp.MustCompile(`xoxp-[0-9]{11}-[0-9]{11}-[0-9a-zA-Z]{24}`),
		severity: "high",
	},
	{
		name:     "Slack Webhook URL",
		pattern:  regexp.MustCompile(`https://hooks\.slack\.com/services/T[0-9A-Z]+/B[0-9A-Z]+/[0-9a-zA-Z]+`),
		severity: "high",
	},
	{
		name:     "SendGrid API Key",
		pattern:  regexp.MustCompile(`SG\.[0-9a-zA-Z_-]{22}\.[0-9a-zA-Z_-]{43}`),
		severity: "high",
	},
	{
		name:     "Twilio Account SID",
		pattern:  regexp.MustCompile(`AC[0-9a-f]{32}`),
		severity: "medium",
	},
	{
		name:     "Twilio Auth Token",
		pattern:  regexp.MustCompile(`(?i)twilio.{0,20}[0-9a-f]{32}`),
		severity: "high",
	},
	{
		name:     "Google API Key",
		pattern:  regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),
		severity: "high",
	},
	{
		name:     "Firebase Server Key",
		pattern:  regexp.MustCompile(`AAAA[A-Za-z0-9_-]{7}:[A-Za-z0-9_-]{140}`),
		severity: "high",
	},
	{
		name:     "npm Token",
		pattern:  regexp.MustCompile(`npm_[0-9a-zA-Z]{36}`),
		severity: "high",
	},
	{
		name:     "PyPI Token",
		pattern:  regexp.MustCompile(`pypi-AgEIcHlwaS5vcmc[0-9a-zA-Z_-]{70,}`),
		severity: "high",
	},
	{
		name:     "Postgres Connection String",
		pattern:  regexp.MustCompile(`postgresql://[^:]+:[^@\s]{8,}@`),
		severity: "high",
	},
	{
		name:     "Neon DB Connection",
		pattern:  regexp.MustCompile(`postgresql://[^:]+:[^@\s]{8,}@[^.]+\.neon\.tech`),
		severity: "high",
	},
	{
		name:     "Private Key Block",
		pattern:  regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |PGP )?PRIVATE KEY`),
		severity: "high",
	},
	{
		name:     "JWT Secret (long hex)",
		pattern:  regexp.MustCompile(`(?i)(jwt.?secret|jwt.?key).{0,10}[0-9a-f]{64}`),
		severity: "high",
	},
	{
		name:     "Generic high-entropy secret",
		pattern:  regexp.MustCompile(`(?i)(password|secret|token|api.?key).{0,5}[=:].{0,5}['\"]?[0-9a-zA-Z+/]{40,}['\"]?`),
		severity: "medium",
	},
}

// filesIgnored are paths that should never be scanned
var dirsIgnored = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".venv": true, "venv": true, "__pycache__": true,
	"dist": true, "build": true, ".next": true, ".nuxt": true,
}

// extensionsIgnored are binary/generated file types
var extensionsIgnored = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".svg": true, ".ico": true, ".woff": true, ".woff2": true,
	".ttf": true, ".eot": true, ".pdf": true, ".zip": true,
	".tar": true, ".gz": true, ".exe": true, ".bin": true,
	".so": true, ".dylib": true, ".dll": true, ".lock": true,
	".sum": true,
}

type scanFinding struct {
	file     string
	line     int
	content  string
	pattern  string
	severity string
}

func scanCmd() *cobra.Command {
	var pathFlag string
	var allFlag bool

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for secrets accidentally left in source files",
		Long: `Scans your project files for secrets that shouldn't be committed to git.

Detects AWS keys, GitHub tokens, Stripe keys, database connection strings,
private keys, API tokens from 20+ services, and high-entropy generic secrets.

This does NOT scan your .env file (that's intentional — .env is local).
It scans everything else: source code, config files, scripts, CI configs.

Run this before every commit, or better, install it as a pre-commit hook:
  echo "dotsync scan" >> .git/hooks/pre-commit
  chmod +x .git/hooks/pre-commit`,
		Example: `  dotsync scan
  dotsync scan --path ./src
  dotsync scan --all  # include .env files in the scan`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := pathFlag
			if root == "" {
				root = "."
			}

			fmt.Printf("🔍 Scanning %s for secrets...\n\n", root)

			var findings []scanFinding
			var filesScanned int

			err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil // skip unreadable files
				}

				// Skip ignored directories
				if info.IsDir() {
					if dirsIgnored[info.Name()] {
						return filepath.SkipDir
					}
					return nil
				}

				// Skip .env files unless --all flag
				if !allFlag && (info.Name() == ".env" ||
					strings.HasPrefix(info.Name(), ".env.")) {
					return nil
				}

				// Skip binary/generated files
				ext := strings.ToLower(filepath.Ext(path))
				if extensionsIgnored[ext] {
					return nil
				}

				// Skip large files (> 1MB)
				if info.Size() > 1024*1024 {
					return nil
				}

				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}

				filesScanned++
				content := string(data)
				lines := strings.Split(content, "\n")

				for _, sp := range secretPatterns {
					for lineNum, line := range lines {
						if sp.pattern.MatchString(line) {
							// Redact the actual secret value in output
							redacted := sp.pattern.ReplaceAllStringFunc(line, func(match string) string {
								if len(match) > 12 {
									return match[:6] + strings.Repeat("*", len(match)-10) + match[len(match)-4:]
								}
								return strings.Repeat("*", len(match))
							})
							findings = append(findings, scanFinding{
								file:     path,
								line:     lineNum + 1,
								content:  strings.TrimSpace(redacted),
								pattern:  sp.name,
								severity: sp.severity,
							})
						}
					}
				}

				return nil
			})

			if err != nil {
				return fmt.Errorf("scan error: %w", err)
			}

			if len(findings) == 0 {
				fmt.Printf("✅ No secrets found in %d files scanned.\n\n", filesScanned)
				fmt.Println("  Good hygiene! Keep secrets in dotsync, not in source code.")
				fmt.Println()
				return nil
			}

			// Group by severity
			var high, medium []scanFinding
			for _, f := range findings {
				if f.severity == "high" {
					high = append(high, f)
				} else {
					medium = append(medium, f)
				}
			}

			fmt.Printf("⚠️  Found %d potential secret(s) in %d file(s) scanned:\n\n",
				len(findings), filesScanned)

			if len(high) > 0 {
				fmt.Println("  🔴 HIGH SEVERITY")
				fmt.Println(strings.Repeat("─", 60))
				for _, f := range high {
					fmt.Printf("  %s:%d\n", f.file, f.line)
					fmt.Printf("    Type    : %s\n", f.pattern)
					fmt.Printf("    Content : %s\n\n", f.content)
				}
			}

			if len(medium) > 0 {
				fmt.Println("  🟡 MEDIUM SEVERITY")
				fmt.Println(strings.Repeat("─", 60))
				for _, f := range medium {
					fmt.Printf("  %s:%d\n", f.file, f.line)
					fmt.Printf("    Type    : %s\n", f.pattern)
					fmt.Printf("    Content : %s\n\n", f.content)
				}
			}

			fmt.Println("  What to do:")
			fmt.Println("  1. Remove the secret from the file immediately")
			fmt.Println("  2. If already committed: rotate the secret, rewrite git history")
			fmt.Println("     git filter-repo --path <file> --invert-paths")
			fmt.Println("  3. Add the secret to dotsync: dotsync push")
			fmt.Println("  4. Reference it via environment variable in your code")
			fmt.Println()

			// Exit 1 so this can be used in CI pipelines
			os.Exit(1)
			return nil
		},
	}

	cmd.Flags().StringVar(&pathFlag, "path", "", "path to scan (default: current directory)")
	cmd.Flags().BoolVar(&allFlag, "all", false, "include .env files in the scan")
	return cmd
}
