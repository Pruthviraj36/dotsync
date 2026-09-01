package cmd

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// ── Pattern definitions ───────────────────────────────────────────────────────
// Each pattern has a name, compiled regex, severity, and an optional context
// validator — a function that looks at the full line for false-positive rejection.

type secretPattern struct {
	name      string
	pattern   *regexp.Regexp
	severity  string
	validator func(line string) bool // nil = always flag
}

// notInComment returns true if the line doesn't look like a comment.
func notInComment(line string) bool {
	t := strings.TrimSpace(line)
	return !strings.HasPrefix(t, "//") &&
		!strings.HasPrefix(t, "#") &&
		!strings.HasPrefix(t, "*") &&
		!strings.HasPrefix(t, "<!--")
}

// hasAssignment returns true if the line contains an assignment operator near
// the match — filters out README examples that just mention key names.
func hasAssignment(line string) bool {
	return strings.ContainsAny(line, "=:'\"(`")
}

var secretPatterns = []secretPattern{
	// ── Cloud provider keys ───────────────────────────────────────────────
	{
		name:     "AWS Access Key ID",
		pattern:  regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		severity: "high",
	},
	{
		name:     "AWS Secret Access Key",
		pattern:  regexp.MustCompile(`(?i)aws[_\-. ]*(secret[_\- ]*access[_\- ]*key|secret[_\- ]*key)\s*[=:]\s*['"]?[0-9a-zA-Z/+]{40}['"]?`),
		severity: "high",
	},
	{
		name:     "Google API Key",
		pattern:  regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`),
		severity: "high",
	},
	{
		name:     "Google OAuth Client Secret",
		pattern:  regexp.MustCompile(`GOCSPX-[0-9A-Za-z\-_]{28}`),
		severity: "high",
	},
	{
		name:     "Firebase Server Key",
		pattern:  regexp.MustCompile(`AAAA[A-Za-z0-9_-]{7}:[A-Za-z0-9_-]{140}`),
		severity: "high",
	},
	{
		name:     "Azure Storage Key",
		pattern:  regexp.MustCompile(`(?i)DefaultEndpointsProtocol=https;AccountName=[^;]+;AccountKey=[A-Za-z0-9+/=]{88}`),
		severity: "high",
	},
	{
		name:     "Azure Client Secret",
		pattern:  regexp.MustCompile(`(?i)(azure|AZURE)[_\-.]*(client[_\-.]secret|secret)[_\-. ]*[=:]\s*['"]?[0-9A-Za-z~._\-]{34,}['"]?`),
		severity: "high",
	},

	// ── Database connection strings ───────────────────────────────────────
	// These are the most commonly hardcoded in NestJS/Express/etc code.
	{
		name:     "MongoDB Connection String",
		pattern:  regexp.MustCompile(`mongodb(\+srv)?://[^:'\"\s]+:[^@'\"\s]{3,}@[^\s'"]+`),
		severity: "high",
	},
	{
		name:     "PostgreSQL Connection String",
		pattern:  regexp.MustCompile(`postgres(ql)?://[^:'\"\s]+:[^@'\"\s]{3,}@[^\s'"]+`),
		severity: "high",
	},
	{
		name:     "MySQL Connection String",
		pattern:  regexp.MustCompile(`mysql://[^:'\"\s]+:[^@'\"\s]{3,}@[^\s'"]+`),
		severity: "high",
	},
	{
		name:     "Redis Connection String",
		pattern:  regexp.MustCompile(`redis(s)?://[^:'\"\s]*:[^@'\"\s]{3,}@[^\s'"]+`),
		severity: "high",
	},
	{
		name:     "Database URI (generic variable)",
		pattern:  regexp.MustCompile(`(?i)(database_url|db_url|mongodb_uri|database_uri|db_uri|connection_string|conn_str)\s*[=:]\s*['"\x60]?(mongodb|postgres|mysql|redis|mssql|sqlite)[^\s'"` + "`" + `]{10,}`),
		severity: "high",
		validator: notInComment,
	},
	// NestJS / TypeORM style: uri: 'mongodb://...' inside useFactory etc.
	{
		name:     "Hardcoded DB URI in code",
		pattern:  regexp.MustCompile(`(?i)\buri\s*:\s*['"\x60](mongodb|postgres|mysql|redis)[^\s'"` + "`" + `]{10,}['"\x60]`),
		severity: "high",
		validator: notInComment,
	},
	// Inline string that IS a connection string (no variable name needed)
	{
		name:     "Embedded Connection String",
		pattern:  regexp.MustCompile(`['"\x60](mongodb(\+srv)?|postgres(ql)?|mysql|redis)://[^:'\"\x60\s]{1,}:[^@'\"\x60\s]{3,}@[^\s'\"\x60]{5,}['"\x60]`),
		severity: "high",
		validator: notInComment,
	},

	// ── GitHub ────────────────────────────────────────────────────────────
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
		name:     "GitHub Fine-Grained Token",
		pattern:  regexp.MustCompile(`github_pat_[0-9a-zA-Z_]{82}`),
		severity: "high",
	},

	// ── Stripe ───────────────────────────────────────────────────────────
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

	// ── Communication APIs ────────────────────────────────────────────────
	{
		name:     "Slack Bot Token",
		pattern:  regexp.MustCompile(`xoxb-[0-9]{8,13}-[0-9]{8,13}-[0-9a-zA-Z]{24}`),
		severity: "high",
	},
	{
		name:     "Slack User Token",
		pattern:  regexp.MustCompile(`xoxp-[0-9]{8,13}-[0-9]{8,13}-[0-9a-zA-Z]{24}`),
		severity: "high",
	},
	{
		name:     "Slack Webhook URL",
		pattern:  regexp.MustCompile(`https://hooks\.slack\.com/services/T[0-9A-Z]{8,}/B[0-9A-Z]{8,}/[0-9a-zA-Z]{24}`),
		severity: "high",
	},
	{
		name:     "SendGrid API Key",
		pattern:  regexp.MustCompile(`SG\.[0-9a-zA-Z_-]{22}\.[0-9a-zA-Z_-]{43}`),
		severity: "high",
	},
	{
		name:     "Twilio Auth Token",
		pattern:  regexp.MustCompile(`(?i)(twilio[_\-. ]*auth[_\-. ]*token|TWILIO_AUTH_TOKEN)\s*[=:]\s*['"]?[0-9a-f]{32}['"]?`),
		severity: "high",
	},
	{
		name:     "Mailgun API Key",
		pattern:  regexp.MustCompile(`key-[0-9a-zA-Z]{32}`),
		severity: "high",
	},
	{
		name:     "Mailchimp API Key",
		pattern:  regexp.MustCompile(`[0-9a-f]{32}-us[0-9]{1,2}`),
		severity: "high",
	},
	{
		name:     "Resend API Key",
		pattern:  regexp.MustCompile(`re_[0-9a-zA-Z_]{20,}`),
		severity: "high",
	},

	// ── Auth tokens ───────────────────────────────────────────────────────
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
		name:     "Cloudinary URL",
		pattern:  regexp.MustCompile(`cloudinary://[0-9]+:[0-9a-zA-Z_-]+@[a-zA-Z0-9]+`),
		severity: "high",
	},
	{
		name:     "Supabase Service Key",
		pattern:  regexp.MustCompile(`eyJ[0-9a-zA-Z_-]{100,}\.[0-9a-zA-Z_-]{50,}\.[0-9a-zA-Z_-]{43}`),
		severity: "high",
		validator: notInComment,
	},

	// ── Private keys / certificates ───────────────────────────────────────
	{
		name:     "Private Key Block",
		pattern:  regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |PGP )?PRIVATE KEY(?: BLOCK)?-----`),
		severity: "high",
	},

	// ── Generic hardcoded credentials ─────────────────────────────────────
	// These match variable assignments with high-entropy values, not just
	// mentions of the word "password" or "secret".
	{
		name:     "Hardcoded Password (assignment)",
		pattern:  regexp.MustCompile(`(?i)\b(password|passwd|pwd)\s*[=:]\s*['"\x60][^\s'"` + "`" + `]{8,}['"\x60]`),
		severity: "medium",
		validator: func(line string) bool {
			t := strings.ToLower(strings.TrimSpace(line))
			// Skip obvious non-secrets: env var references, placeholders
			if strings.Contains(t, "process.env") ||
				strings.Contains(t, "os.getenv") ||
				strings.Contains(t, "config.get") ||
				strings.Contains(t, "configservice") ||
				strings.Contains(t, "your-password") ||
				strings.Contains(t, "changeme") ||
				strings.Contains(t, "placeholder") ||
				strings.Contains(t, "example") {
				return false
			}
			return notInComment(line)
		},
	},
	{
		name:     "Hardcoded API Key (assignment)",
		pattern:  regexp.MustCompile(`(?i)\b(api[_\-.]?key|apikey|access[_\-.]?key)\s*[=:]\s*['"\x60][^\s'"` + "`" + `]{16,}['"\x60]`),
		severity: "high",
		validator: func(line string) bool {
			t := strings.ToLower(strings.TrimSpace(line))
			return !strings.Contains(t, "process.env") &&
				!strings.Contains(t, "os.getenv") &&
				!strings.Contains(t, "config.get") &&
				!strings.Contains(t, "configservice") &&
				!strings.Contains(t, "your_api_key") &&
				!strings.Contains(t, "placeholder") &&
				notInComment(line)
		},
	},
	{
		name:     "Hardcoded Secret (assignment)",
		pattern:  regexp.MustCompile(`(?i)\b(jwt[_\-.]?secret|app[_\-.]?secret|secret[_\-.]?key|signing[_\-.]?key|encryption[_\-.]?key)\s*[=:]\s*['"\x60][^\s'"` + "`" + `]{16,}['"\x60]`),
		severity: "high",
		validator: func(line string) bool {
			t := strings.ToLower(strings.TrimSpace(line))
			return !strings.Contains(t, "process.env") &&
				!strings.Contains(t, "os.getenv") &&
				!strings.Contains(t, "config.get") &&
				!strings.Contains(t, "configservice") &&
				notInComment(line)
		},
	},
}

// ── Entropy scanner ───────────────────────────────────────────────────────────
// High-entropy strings assigned to sensitive variable names — catches secrets
// that don't match known formats. Uses Shannon entropy threshold of 4.5.

type entropyFinding struct {
	value    string
	varName  string
	entropy  float64
}

var entropyVarPattern = regexp.MustCompile(
	`(?i)\b(token|secret|key|password|passwd|pwd|credential|auth|api_key|access_key|private_key)\s*[=:]\s*['"\x60]([A-Za-z0-9+/=_\-!@#$%^&*]{16,})['"\x60]`,
)

func shannonEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	freq := make(map[rune]int)
	for _, c := range s {
		freq[c]++
	}
	n := float64(len(s))
	var h float64
	for _, count := range freq {
		p := float64(count) / n
		h -= p * math.Log2(p)
	}
	return h
}

func findHighEntropyStrings(line string) []entropyFinding {
	matches := entropyVarPattern.FindAllStringSubmatch(line, -1)
	var found []entropyFinding
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		varName := m[1]
		value := m[2]
		// Skip obvious placeholders
		lower := strings.ToLower(value)
		if strings.Contains(lower, "example") ||
			strings.Contains(lower, "placeholder") ||
			strings.Contains(lower, "changeme") ||
			strings.Contains(lower, "your_") ||
			value == strings.Repeat(value[0:1], len(value)) { // all same char
			continue
		}
		// Skip environment variable references
		fullLine := strings.ToLower(line)
		if strings.Contains(fullLine, "process.env") ||
			strings.Contains(fullLine, "os.getenv") ||
			strings.Contains(fullLine, "configservice") {
			continue
		}
		entropy := shannonEntropy(value)
		if entropy >= 4.5 {
			found = append(found, entropyFinding{
				value:   value,
				varName: varName,
				entropy: entropy,
			})
		}
	}
	return found
}

// ── Ignored dirs and extensions ───────────────────────────────────────────────

var dirsIgnored = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".venv": true, "venv": true, "env": true, "__pycache__": true,
	"dist": true, "build": true, ".next": true, ".nuxt": true,
	".turbo": true, "coverage": true, ".nyc_output": true,
	"target": true, // Rust/Java
}

var extsIgnored = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".svg": true, ".ico": true, ".woff": true, ".woff2": true,
	".ttf": true, ".eot": true, ".pdf": true, ".zip": true,
	".tar": true, ".gz": true, ".exe": true, ".bin": true,
	".so": true, ".dylib": true, ".dll": true, ".lock": true,
	".sum": true, ".map": true, ".min.js": true,
}

// ── Finding struct ────────────────────────────────────────────────────────────

type scanFinding struct {
	file     string
	line     int
	content  string
	pattern  string
	severity string
	entropy  float64 // > 0 means entropy-detected
}

// ── Command ───────────────────────────────────────────────────────────────────

func scanCmd() *cobra.Command {
	var pathFlag string
	var allFlag bool

	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan source files for accidentally committed secrets",
		Long: `Scans every file in your project for hardcoded secrets.

Detection methods:
  1. Pattern matching — 30+ known formats (AWS, GCP, Stripe, MongoDB URIs,
     GitHub tokens, private keys, Slack webhooks, and more)
  2. Entropy analysis — flags high-entropy strings assigned to variables
     named token, secret, key, password, etc. even if format is unknown

Skips .env files by default (add --all to include them).
Exits with code 1 if anything is found — drop-in CI and pre-commit hook.

  echo "dotsync scan" >> .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit`,
		Example: `  dotsync scan
  dotsync scan --path ./src
  dotsync scan --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := pathFlag
			if root == "" {
				root = "."
			}

			blank()
			fmt.Println(step(fmt.Sprintf("scanning %s", root)))
			blank()

			var findings []scanFinding
			var filesScanned int
			var filesSkipped int

			filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if info.IsDir() {
					if dirsIgnored[info.Name()] {
						filesSkipped++
						return filepath.SkipDir
					}
					return nil
				}
				if !allFlag && (info.Name() == ".env" || strings.HasPrefix(info.Name(), ".env.")) {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(path))
				if extsIgnored[ext] {
					return nil
				}
				if strings.HasSuffix(path, ".min.js") || strings.HasSuffix(path, ".min.css") {
					return nil
				}
				if info.Size() > 512*1024 { // skip files > 512KB
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}
				filesScanned++
				lines := strings.Split(string(data), "\n")

				// Pass 1: known patterns
				for _, sp := range secretPatterns {
					for lineNum, line := range lines {
						if !sp.pattern.MatchString(line) {
							continue
						}
						if sp.validator != nil && !sp.validator(line) {
							continue
						}
						redacted := sp.pattern.ReplaceAllStringFunc(line, redactSecret)
						findings = append(findings, scanFinding{
							file:     path,
							line:     lineNum + 1,
							content:  strings.TrimSpace(redacted),
							pattern:  sp.name,
							severity: sp.severity,
						})
					}
				}

				// Pass 2: entropy analysis
				for lineNum, line := range lines {
					if strings.TrimSpace(line) == "" {
						continue
					}
					// Skip lines already caught by pattern matching
					alreadyCaught := false
					for _, f := range findings {
						if f.file == path && f.line == lineNum+1 {
							alreadyCaught = true
							break
						}
					}
					if alreadyCaught {
						continue
					}
					for _, ef := range findHighEntropyStrings(line) {
						redacted := redactSecret(ef.value)
						findings = append(findings, scanFinding{
							file:     path,
							line:     lineNum + 1,
							content:  strings.TrimSpace(strings.Replace(line, ef.value, redacted, 1)),
							pattern:  fmt.Sprintf("High-entropy %s (%.1f bits)", ef.varName, ef.entropy),
							severity: "medium",
							entropy:  ef.entropy,
						})
					}
				}

				return nil
			})

			if len(findings) == 0 {
				fmt.Println(ok(fmt.Sprintf("clean — no secrets in %d files", filesScanned)))
				blank()
				hint("keep secrets in dotsync, not in source code")
				blank()
				return nil
			}

			// Deduplicate (same file+line can match multiple patterns)
			findings = deduplicateFindings(findings)

			var high, medium []scanFinding
			for _, f := range findings {
				if f.severity == "high" {
					high = append(high, f)
				} else {
					medium = append(medium, f)
				}
			}

			blank()
			fmt.Println(warn(fmt.Sprintf("%d secret(s) found across %d files scanned", len(findings), filesScanned)))
			blank()

			indent := msgPad()
			ruler := ruleN(termWidth() - (verbCol + verbGap) - 2)

			printFindings := func(severity string, sevFn func(string) string, fs []scanFinding) {
				if len(fs) == 0 {
					return
				}
				fmt.Printf("%s%s  %s\n", indent, sevFn("■"), bold(severity))
				fmt.Println(indent + ruler)
				for _, f := range fs {
					fmt.Printf("%s%s  %s\n",
						indent,
						yellow(fmt.Sprintf("line %-4d", f.line)),
						bold(f.file),
					)
					fmt.Printf("%s%s  %s\n",
						indent,
						padRight("", 9),
						cyan(f.pattern),
					)
					if f.content != "" {
						truncated := f.content
						maxW := termWidth() - (verbCol + verbGap) - 8
						if len(truncated) > maxW {
							truncated = truncated[:maxW] + dim("…")
						}
						fmt.Printf("%s%s  %s\n",
							indent,
							padRight("", 9),
							dim(truncated),
						)
					}
					blank()
				}
				fmt.Println(indent + ruler)
				blank()
			}

			printFindings("HIGH SEVERITY", red, high)
			printFindings("MEDIUM SEVERITY", yellow, medium)

			// Remediation
			fmt.Printf("%s%s\n", indent, bold("Remediation"))
			fmt.Println(indent + ruler)
			blank()
			hint("1. Remove the secret from the file immediately")
			hint("2. Rotate it — treat it as compromised")
			hint("3. Rewrite git history if already committed:")
			cmdHint("     git filter-repo --path <file> --invert-paths")
			hint("4. Store it safely:")
			cmdHint("     dotsync push")
			hint("5. Read it via environment variable in your code")
			blank()

			os.Exit(1)
			return nil
		},
	}

	cmd.Flags().StringVar(&pathFlag, "path", "", "directory to scan (default: .)")
	cmd.Flags().BoolVar(&allFlag, "all", false, "include .env files in scan")
	return cmd
}

func redactSecret(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	keep := 4
	if len(s) > 20 {
		keep = 6
	}
	return s[:keep] + strings.Repeat("*", len(s)-keep*2) + s[len(s)-keep:]
}

func deduplicateFindings(findings []scanFinding) []scanFinding {
	seen := make(map[string]bool)
	out := findings[:0]
	for _, f := range findings {
		key := fmt.Sprintf("%s:%d:%s", f.file, f.line, f.pattern)
		if !seen[key] {
			seen[key] = true
			out = append(out, f)
		}
	}
	return out
}
