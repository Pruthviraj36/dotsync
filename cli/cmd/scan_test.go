package cmd

import (
	"testing"
)

// ── Pattern matching tests ────────────────────────────────────────────────────

type patternCase struct {
	name    string
	line    string
	wantHit bool
	pattern string // which pattern should fire (empty = any)
}

var patternCases = []patternCase{
	// AWS
	{
		name:    "AWS access key",
		line:    `aws_access_key_id = AKIAIOSFODNN7EXAMPLE`,
		wantHit: true,
		pattern: "AWS Access Key ID",
	},
	{
		name:    "AWS access key in JS",
		line:    `const key = "AKIAIOSFODNN7EXAMPLE";`,
		wantHit: true,
		pattern: "AWS Access Key ID",
	},
	{
		name:    "AWS access key in comment — should miss",
		line:    `// AKIAIOSFODNN7EXAMPLE is an example key`,
		wantHit: false,
	},

	// MongoDB
	{
		name:    "MongoDB URI hardcoded in NestJS useFactory",
		line:    `uri: 'mongodb+srv://' + 'admin:s3cr3tPass@cluster0.abc.mongodb.net/mydb',`,
		wantHit: true,
		pattern: "Embedded Connection String",
	},
	{
		name:    "MongoDB URI in variable assignment",
		line:    `const uri = "mongodb://user:hunter2' + '@localhost:27017/mydb"`,
		wantHit: true,
		pattern: "Embedded Connection String",
	},
	{
		name:    "MongoDB URI via configService — should miss",
		line:    `uri: configService.get<string>('DATABASE_URI'),`,
		wantHit: false,
	},
	{
		name:    "MongoDB URI via process.env — should miss",
		line:    `uri: process.env.DATABASE_URL,`,
		wantHit: false,
	},

	// PostgreSQL
	{
		name:    "Postgres connection string",
		line:    `DATABASE_URL=postgresql://myuser:mypass@' + 'localhost:5432/mydb`,
		wantHit: true,
		pattern: "PostgreSQL Connection String",
	},
	{
		name:    "Postgres in Go source",
		line:    `db, _ := sql.Open("postgres", "postgres://admin:secret123' + '@db.example.com/app")`,
		wantHit: true,
		pattern: "Embedded Connection String",
	},

	// GitHub tokens
	{
		name:    "GitHub PAT",
		line:    `token: ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ123456`,
		wantHit: true,
		pattern: "GitHub Personal Access Token",
	},
	{
		name:    "GitHub fine-grained token",
		line:    `GITHUB_TOKEN=github_pat_11AB' + 'CDEFG0abcdefghij' + 'klmnopqrstuvwxyzABCDEFGHIJKLM' + 'NOPQRSTUVWXYZ0123456789abcd`,
		wantHit: true,
		pattern: "GitHub Fine-Grained Token",
	},
	{
		name:    "GitHub Actions token",
		line:    `token = ghs_aBcDeFgHiJkLmN' + 'oPqRsTuVwXyZ123456`,
		wantHit: true,
		pattern: "GitHub Actions Token",
	},

	// Stripe
	{
		name:    "Stripe live secret key",
		line:    `STRIPE_SECRET_KEY=sk' + '_live_4e' + 'C39HqLyjWDarjtT1zdp7dc`,
		wantHit: true,
		pattern: "Stripe Secret Key (live)",
	},
	{
		name:    "Stripe test key — medium severity",
		line:    `const stripe = Stripe("sk' + '_test_4eC' + '39HqLyjWDarjtT1zdp7dc")`,
		wantHit: true,
		pattern: "Stripe Secret Key (test)",
	},

	// Private key
	{
		name:    "RSA private key header",
		line:    `-----BEGIN RSA PRIVATE KEY-----`,
		wantHit: true,
		pattern: "Private Key Block",
	},
	{
		name:    "OpenSSH private key header",
		line:    `-----BEGIN OPENSSH PRIVATE KEY-----`,
		wantHit: true,
		pattern: "Private Key Block",
	},

	// Generic hardcoded credentials
	{
		name:    "Hardcoded password assignment",
		line:    `password: 'hunter2_ultra_secret',`,
		wantHit: true,
		pattern: "Hardcoded Password (assignment)",
	},
	{
		name:    "Password from env — should miss",
		line:    `password: process.env.DB_PASSWORD,`,
		wantHit: false,
	},
	{
		name:    "Password placeholder — should miss",
		line:    `password: 'your-password-here',`,
		wantHit: false,
	},
	{
		name:    "JWT secret hardcoded",
		line:    `jwt_secret: "a1b2c3d4e5' + 'f6a7b8c9d0e1f2a3b4c5d6"`,
		wantHit: true,
		pattern: "Hardcoded Secret (assignment)",
	},
	{
		name:    "JWT secret from configService — should miss",
		line:    `secret: configService.get('JWT_SECRET'),`,
		wantHit: false,
	},

	// Slack
	{
		name:    "Slack bot token",
		line:    `SLACK' + '_TOKEN=xo' + 'xb-12345678-12' + '345678-aBcDeFgHiJ' + 'kLmNoPqRsTuVw`,
		wantHit: true,
		pattern: "Slack Bot Token",
	},
	{
		name:    "Slack webhook URL",
		line:    `web' + 'hook = "https://hooks' + '.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX"`,
		wantHit: true,
		pattern: "Slack Webhook URL",
	},

	// Google
	{
		name:    "Google API key",
		line:    `const apiKey = "AIzaSyD-9t' + 'Srke72PouQMnMX-' + 'a7eZSW0jkFMBWY"`,
		wantHit: true,
		pattern: "Google API Key",
	},

	// SendGrid
	{
		name:    "SendGrid API key",
		line:    `SG.aBcDeF' + 'gHiJkLmNoPqRsTuVw.xYzA' + 'bCdEfGhIjKlMnOpQrSt' + 'UvWxYzAbCdEfGhIjKl`,
		wantHit: true,
		pattern: "SendGrid API Key",
	},

	// Database URI generic var
	{
		name:    "DATABASE_URL env var",
		line:    `DATABASE_URL=postgres://user:pass@host/db`,
		wantHit: true,
	},

	// NestJS-style hardcoded URI in code
	{
		name:    "NestJS uri: 'mongodb+srv://...'",
		line:    `            uri: 'mongodb+srv://user:pass@cluster.net/db',`,
		wantHit: true,
		pattern: "Hardcoded DB URI in code",
	},
	{
		name:    "NestJS uri: configService — should miss",
		line:    `            uri: configService.get<string>('MONGO_URI'),`,
		wantHit: false,
	},
}

func TestPatternMatching(t *testing.T) {
	for _, tc := range patternCases {
		t.Run(tc.name, func(t *testing.T) {
			hit := false
			hitPattern := ""
			for _, sp := range secretPatterns {
				if !sp.pattern.MatchString(tc.line) {
					continue
				}
				if sp.validator != nil && !sp.validator(tc.line) {
					continue
				}
				hit = true
				hitPattern = sp.name
				break
			}

			if tc.wantHit && !hit {
				t.Errorf("expected pattern to match but got no hit\n  line: %s", tc.line)
			}
			if !tc.wantHit && hit {
				t.Errorf("expected no match but got hit: %s\n  line: %s", hitPattern, tc.line)
			}
			if tc.wantHit && tc.pattern != "" && hitPattern != tc.pattern {
				t.Errorf("expected pattern %q but got %q\n  line: %s", tc.pattern, hitPattern, tc.line)
			}
		})
	}
}

// ── Entropy tests ─────────────────────────────────────────────────────────────

func TestShannonEntropy(t *testing.T) {
	cases := []struct {
		input   string
		wantMin float64
		wantMax float64
		desc    string
	}{
		{"aaaaaaaaaaaaaaaa", 0.0, 0.5, "all same char — near zero entropy"},
		{"abcdefghijklmnop", 3.9, 4.1, "sequential alphabet — ~4 bits"},
		{"aB3$kLm9pQr7xZv2", 3.8, 4.5, "mixed random-looking — high entropy"},
		{"s3cr3tK3yV4lu3!!", 3.5, 4.5, "leetspeak secret — high entropy"},
		{"password", 2.7, 3.1, "common word — moderate entropy"},
		{"X7kP2mQ9vR4tY8nL3cW6zA1bH5jF0sD", 4.0, 5.0, "high-entropy secret — very high"},
		{"AKIAIOSFODNN7EXAMPLE", 3.8, 4.5, "AWS key pattern — high"},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := shannonEntropy(tc.input)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("shannonEntropy(%q) = %.2f, want [%.1f, %.1f]",
					tc.input, got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestEntropyDetection(t *testing.T) {
	cases := []struct {
		line    string
		wantHit bool
		desc    string
	}{
		{
			line:    `const secret = "a1b2c' + '3d4e5f6g7h8i9j0k' + '1l2m3n4o5p6"`,
			wantHit: true,
			desc:    "high-entropy secret variable",
		},
		{
			line:    `const token = "xvzT9k' + 'LmP3qR7sNwB5cF2dY' + 'jH8aEgKiU"`,
			wantHit: true,
			desc:    "high-entropy token variable",
		},
		{
			line:    `const token = process.env.MY_TOKEN`,
			wantHit: false,
			desc:    "token from env var — should miss",
		},
		{
			line:    `password: 'changeme'`,
			wantHit: false,
			desc:    "placeholder password — should miss",
		},
		{
			line:    `const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
			wantHit: false,
			desc:    "all same char — low entropy, should miss",
		},
		{
			line:    `api_key: configService.get('API_KEY')`,
			wantHit: false,
			desc:    "key from configService — should miss",
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			findings := findHighEntropyStrings(tc.line)
			hit := len(findings) > 0
			if tc.wantHit && !hit {
				t.Errorf("expected entropy hit but got none\n  line: %s", tc.line)
			}
			if !tc.wantHit && hit {
				t.Errorf("expected no entropy hit but got: %+v\n  line: %s", findings, tc.line)
			}
		})
	}
}

// ── Container env injection tests ─────────────────────────────────────────────

func TestInjectContainerEnv(t *testing.T) {
	secrets := map[string]string{
		"DB_URL": "postgres://localhost/test",
		"TOKEN":  "abc123",
	}

	cases := []struct {
		name      string
		args      []string
		wantImage string // should appear after all --env flags
		wantRun   bool   // injection should happen
	}{
		{
			name:      "basic run",
			args:      []string{"run", "myimage"},
			wantImage: "myimage",
			wantRun:   true,
		},
		{
			name:      "run with -d flag",
			args:      []string{"run", "-d", "myimage"},
			wantImage: "myimage",
			wantRun:   true,
		},
		{
			name:      "run with --pull=never",
			args:      []string{"run", "--pull=never", "myimage"},
			wantImage: "myimage",
			wantRun:   true,
		},
		{
			name:      "run with -d --pull=never",
			args:      []string{"run", "-d", "--pull=never", "myimage"},
			wantImage: "myimage",
			wantRun:   true,
		},
		{
			name:      "run with localhost/ image",
			args:      []string{"run", "-d", "--pull=never", "localhost/myapp:v1.0"},
			wantImage: "localhost/myapp:v1.0",
			wantRun:   true,
		},
		{
			name:      "run with -p port mapping",
			args:      []string{"run", "-p", "8080:8080", "myimage"},
			wantImage: "myimage",
			wantRun:   true,
		},
		{
			name:    "build subcommand — no injection",
			args:    []string{"build", "."},
			wantRun: false,
		},
		{
			name:    "push subcommand — no injection",
			args:    []string{"push", "myimage"},
			wantRun: false,
		},
		{
			name:    "empty args — no injection",
			args:    []string{},
			wantRun: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := injectContainerEnv(tc.args, secrets)

			if !tc.wantRun {
				if len(result) != len(tc.args) {
					t.Errorf("expected no modification but got: %v", result)
				}
				return
			}

			// Count --env flags injected
			envCount := 0
			for _, a := range result {
				if a == "--env" {
					envCount++
				}
			}
			if envCount != len(secrets) {
				t.Errorf("expected %d --env flags, got %d\n  result: %v", len(secrets), envCount, result)
			}

			// Image name must appear after all --env flags
			if tc.wantImage != "" {
				lastEnvIdx := -1
				imageIdx := -1
				for i, a := range result {
					if a == "--env" {
						lastEnvIdx = i + 1 // the value after --env
					}
					if a == tc.wantImage {
						imageIdx = i
					}
				}
				if imageIdx < 0 {
					t.Errorf("image %q not found in result: %v", tc.wantImage, result)
				} else if imageIdx <= lastEnvIdx {
					t.Errorf("image %q appears before --env values\n  result: %v", tc.wantImage, result)
				}
			}
		})
	}
}

// ── Secret redaction tests ────────────────────────────────────────────────────

func TestRedactSecret(t *testing.T) {
	cases := []struct {
		input    string
		wantLen  int
		wantKeep string // prefix that should be visible
	}{
		{"sk' + '_live_abcdef' + 'ghijklmnop", 23, "sk_li"},
		{"AKIAIOSFODNN7EXAMPLE", 20, "AKIA"},
		{"abc", 3, ""},
		{"abcdefgh", 8, ""},
	}

	for _, tc := range cases {
		t.Run(tc.input[:min(8, len(tc.input))], func(t *testing.T) {
			got := redactSecret(tc.input)
			if len(got) != tc.wantLen {
				t.Errorf("redactSecret(%q) len = %d, want %d (got %q)", tc.input, len(got), tc.wantLen, got)
			}
			if tc.wantKeep != "" && len(got) >= len(tc.wantKeep) {
				if got[:len(tc.wantKeep)] != tc.wantKeep {
					t.Errorf("redactSecret(%q) prefix = %q, want %q", tc.input, got[:len(tc.wantKeep)], tc.wantKeep)
				}
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
