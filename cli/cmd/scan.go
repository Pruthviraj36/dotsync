package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

type secretPattern struct {
	name     string
	pattern  *regexp.Regexp
	severity string
}

var secretPatterns = []secretPattern{
	{"AWS Access Key",              regexp.MustCompile(`(?i)AKIA[0-9A-Z]{16}`),                                                           "high"},
	{"AWS Secret Key",              regexp.MustCompile(`(?i)aws.{0,20}['""][0-9a-zA-Z/+]{40}["'']`),                                      "high"},
	{"GitHub Personal Token",       regexp.MustCompile(`ghp_[0-9a-zA-Z]{36}`),                                                            "high"},
	{"GitHub OAuth Token",          regexp.MustCompile(`gho_[0-9a-zA-Z]{36}`),                                                            "high"},
	{"GitHub Actions Token",        regexp.MustCompile(`ghs_[0-9a-zA-Z]{36}`),                                                            "high"},
	{"Stripe Secret Key (live)",    regexp.MustCompile(`sk_live_[0-9a-zA-Z]{24,}`),                                                       "high"},
	{"Stripe Secret Key (test)",    regexp.MustCompile(`sk_test_[0-9a-zA-Z]{24,}`),                                                       "medium"},
	{"Stripe Restricted Key",       regexp.MustCompile(`rk_live_[0-9a-zA-Z]{24,}`),                                                       "high"},
	{"Stripe Webhook Secret",       regexp.MustCompile(`whsec_[0-9a-zA-Z]{32,}`),                                                         "high"},
	{"Slack Bot Token",             regexp.MustCompile(`xoxb-[0-9]{11}-[0-9]{11}-[0-9a-zA-Z]{24}`),                                       "high"},
	{"Slack User Token",            regexp.MustCompile(`xoxp-[0-9]{11}-[0-9]{11}-[0-9a-zA-Z]{24}`),                                       "high"},
	{"Slack Webhook URL",           regexp.MustCompile(`https://hooks\.slack\.com/services/T[0-9A-Z]+/B[0-9A-Z]+/[0-9a-zA-Z]+`),          "high"},
	{"SendGrid API Key",            regexp.MustCompile(`SG\.[0-9a-zA-Z_-]{22}\.[0-9a-zA-Z_-]{43}`),                                      "high"},
	{"Twilio Account SID",          regexp.MustCompile(`AC[0-9a-f]{32}`),                                                                 "medium"},
	{"Twilio Auth Token",           regexp.MustCompile(`(?i)twilio.{0,20}[0-9a-f]{32}`),                                                  "high"},
	{"Google API Key",              regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),                                                          "high"},
	{"Firebase Server Key",         regexp.MustCompile(`AAAA[A-Za-z0-9_-]{7}:[A-Za-z0-9_-]{140}`),                                       "high"},
	{"npm Token",                   regexp.MustCompile(`npm_[0-9a-zA-Z]{36}`),                                                            "high"},
	{"PyPI Token",                  regexp.MustCompile(`pypi-AgEIcHlwaS5vcmc[0-9a-zA-Z_-]{70,}`),                                         "high"},
	{"Postgres Connection String",  regexp.MustCompile(`postgresql://[^:]+:[^@\s]{8,}@`),                                                 "high"},
	{"Private Key Block",           regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |PGP )?PRIVATE KEY`),                                "high"},
	{"JWT Secret (hex)",            regexp.MustCompile(`(?i)(jwt.?secret|jwt.?key).{0,10}[0-9a-f]{64}`),                                  "high"},
	{"Generic high-entropy secret", regexp.MustCompile(`(?i)(password|secret|token|api.?key).{0,5}[=:].{0,5}['"]?[0-9a-zA-Z+/]{40,}['"]?`), "medium"},
}

var dirsIgnored = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".venv": true, "venv": true, "__pycache__": true,
	"dist": true, "build": true, ".next": true, ".nuxt": true,
}

var extsIgnored = map[string]bool{
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
		Long: `Scans project files for secrets that shouldn't be in git.

Detects AWS keys, GitHub tokens, Stripe keys, database URLs, private keys,
and tokens from 20+ services. Values are redacted in output.

Skips .env files by default (add --all to include them).
Exits with code 1 if anything is found — useful in CI and pre-commit hooks.

Install as a git pre-commit hook:
  echo "dotsync scan" >> .git/hooks/pre-commit
  chmod +x .git/hooks/pre-commit`,
		Example: `  dotsync scan
  dotsync scan --path ./src
  dotsync scan --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := pathFlag
			if root == "" {
				root = "."
			}

			blank()
			fmt.Println(spin(fmt.Sprintf("Scanning %s...", root)))
			blank()

			var findings []scanFinding
			var filesScanned int

			filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if info.IsDir() {
					if dirsIgnored[info.Name()] {
						return filepath.SkipDir
					}
					return nil
				}
				if !allFlag && (info.Name() == ".env" || strings.HasPrefix(info.Name(), ".env.")) {
					return nil
				}
				if extsIgnored[strings.ToLower(filepath.Ext(path))] {
					return nil
				}
				if info.Size() > 1024*1024 {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}
				filesScanned++
				lines := strings.Split(string(data), "\n")
				for _, sp := range secretPatterns {
					for lineNum, line := range lines {
						if sp.pattern.MatchString(line) {
							redacted := sp.pattern.ReplaceAllStringFunc(line, func(m string) string {
								if len(m) > 12 {
									return m[:6] + strings.Repeat("*", len(m)-10) + m[len(m)-4:]
								}
								return strings.Repeat("*", len(m))
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

			if len(findings) == 0 {
				fmt.Println(ok(fmt.Sprintf("Clean — no secrets found in %d files.", filesScanned)))
				blank()
				hint("Good hygiene. Keep secrets in dotsync, not in source code.")
				blank()
				return nil
			}

			var high, medium []scanFinding
			for _, f := range findings {
				if f.severity == "high" {
					high = append(high, f)
				} else {
					medium = append(medium, f)
				}
			}

			fmt.Println(warn(fmt.Sprintf("%d potential secret(s) in %d files scanned",
				len(findings), filesScanned)))
			blank()

			printFindings := func(label string, labelFn func(string) string, fs []scanFinding) {
				if len(fs) == 0 {
					return
				}
				rl := ruleN(60)
				fmt.Printf("  %s\n", labelFn(bold(label)))
				fmt.Println("  " + rl)
				for _, f := range fs {
					fmt.Printf("  %s  %s\n", bold(f.file), yellow(fmt.Sprintf("line %d", f.line)))
					fmt.Printf("  %s  %s\n", padRight("", 2), colDim("Type", 6)+"  "+cyan(f.pattern))
					fmt.Printf("  %s  %s\n", padRight("", 2), colDim("Value", 6)+"  "+dim(f.content))
					blank()
				}
				fmt.Println("  " + rl)
				blank()
			}

			printFindings("HIGH SEVERITY", red, high)
			printFindings("MEDIUM SEVERITY", yellow, medium)

			rl := ruleN(60)
			fmt.Println("  " + rl)
			fmt.Printf("  %s\n", bold("What to do"))
			fmt.Println("  " + rl)
			blank()
			hint("1. Remove the secret from the file immediately")
			hint("2. If already committed, rotate the secret and rewrite git history:")
			cmdHint("   git filter-repo --path <file> --invert-paths")
			hint("3. Store it in dotsync instead:")
			cmdHint("   dotsync push")
			hint("4. Reference it as an environment variable in your code")
			blank()

			os.Exit(1)
			return nil
		},
	}

	cmd.Flags().StringVar(&pathFlag, "path", "", "directory to scan (default: .)")
	cmd.Flags().BoolVar(&allFlag, "all", false, "include .env files in the scan")
	return cmd
}
