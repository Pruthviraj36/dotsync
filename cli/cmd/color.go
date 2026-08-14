package cmd

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ── ANSI codes ────────────────────────────────────────────────────────────────

const (
	cReset        = "\033[0m"
	cBold         = "\033[1m"
	cDim          = "\033[2m"
	cItalic       = "\033[3m"
	cGreen        = "\033[32m"
	cYellow       = "\033[33m"
	cRed          = "\033[31m"
	cCyan         = "\033[36m"
	cBlue         = "\033[34m"
	cMagenta      = "\033[35m"
	cWhite        = "\033[97m"
	cBrightGreen  = "\033[92m"
	cBrightCyan   = "\033[96m"
	cBrightRed    = "\033[91m"
	cBrightYellow = "\033[93m"
)

var isTTY = term.IsTerminal(int(os.Stdout.Fd()))

func colorize(code, s string) string {
	if !isTTY {
		return s
	}
	return code + s + cReset
}

// ── Color primitives ──────────────────────────────────────────────────────────

func green(s string) string     { return colorize(cBrightGreen, s) }
func yellow(s string) string    { return colorize(cBrightYellow, s) }
func red(s string) string       { return colorize(cBrightRed, s) }
func cyan(s string) string      { return colorize(cBrightCyan, s) }
func blue(s string) string      { return colorize(cBlue, s) }
func magenta(s string) string   { return colorize(cMagenta, s) }
func bold(s string) string      { return colorize(cBold, s) }
func dim(s string) string       { return colorize(cDim, s) }
func italic(s string) string    { return colorize(cItalic, s) }
func boldCyan(s string) string  { return colorize(cBold+cBrightCyan, s) }
func boldGreen(s string) string { return colorize(cBold+cBrightGreen, s) }
func boldRed(s string) string   { return colorize(cBold+cBrightRed, s) }

// ── Status line system ────────────────────────────────────────────────────────
//
// Inspired by cargo, bun, flatpak — a right-aligned bold label in a fixed
// column, followed by the message. Labels are right-aligned to 12 chars.
//
// TTY output:
//   ✓     Encrypting  my-app/dev — 12 secrets
//   ✓      Uploading  1.2 KB
//   ✓         Pushed  my-app/dev → v7
//   ✗          Error  file not found: .env
//   !        Warning  .env already exists, overwrite? [y/N]
//   →           Info  signature verified (alice, ed25519)
//
// The label column is always 12 chars right-aligned. Message starts at col 16.
// This matches how `cargo build` looks — clean, scannable, professional.

const labelW = 12 // visual width of the right-aligned label

// label builds a right-aligned status label.
func label(s string, colorFn func(string) string) string {
	pad := labelW - len(s) // s has no ANSI here, safe to use len
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + colorFn(s)
}

// ok — completed successfully. Right-aligned green label.
func ok(s string) string {
	return label("done", boldGreen) + "  " + s
}

// step — in-progress operation. Dim label, no glyph noise.
func step(s string) string {
	return label("·", dim) + "  " + dim(s)
}

// spin is an alias for step.
func spin(s string) string { return step(s) }

// fail — hard error.
func fail(s string) string {
	return label("error", boldRed) + "  " + s
}

// warn — non-fatal warning.
func warn(s string) string {
	return label("warning", yellow) + "  " + s
}

// info — informational.
func info(s string) string {
	return label("info", cyan) + "  " + s
}

// prog — a named build/operation step. Like cargo's "Compiling foo v1.0".
// label is right-aligned bold cyan (the verb), msg is the detail.
func prog(verb, msg string) string {
	return label(verb, boldCyan) + "  " + msg
}

// ── Terminal geometry ─────────────────────────────────────────────────────────

func termWidth() int {
	if !isTTY {
		return 80
	}
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 60 {
		return 80
	}
	if w > 100 {
		return 100
	}
	return w
}

func ruleN(n int) string { return dim(strings.Repeat("─", n)) }

func rl() string { return ruleN(termWidth() - 2) }

// ── ANSI-aware text measurement ───────────────────────────────────────────────

func visibleLen(s string) int {
	inEsc := false
	n := 0
	for _, r := range s {
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r == '\033' {
			inEsc = true
			continue
		}
		n++
	}
	return n
}

func padRight(s string, width int) string {
	if p := width - visibleLen(s); p > 0 {
		return s + strings.Repeat(" ", p)
	}
	return s
}

func col(raw string, width int, colorFn func(string) string) string {
	return padRight(colorFn(raw), width)
}

func colDim(raw string, width int) string   { return col(raw, width, dim) }
func colCyan(raw string, width int) string  { return col(raw, width, cyan) }
func colGreen(raw string, width int) string { return col(raw, width, green) }
func colBold(raw string, width int) string  { return col(raw, width, bold) }

// ── Key-value pairs ───────────────────────────────────────────────────────────
// Labels right-aligned to labelW — same column as status labels above,
// so the whole output shares one vertical rhythm.

func kv(lbl, value string) {
	fmt.Printf("%s  %s\n", label(lbl, bold), value)
}
func kvGreen(lbl, value string) {
	fmt.Printf("%s  %s\n", label(lbl, bold), green(value))
}
func kvCyan(lbl, value string) {
	fmt.Printf("%s  %s\n", label(lbl, bold), cyan(value))
}
func kvDim(lbl, value string) {
	fmt.Printf("%s  %s\n", label(lbl, bold), dim(value))
}
func kvRed(lbl, value string) {
	fmt.Printf("%s  %s\n", label(lbl, bold), red(value))
}

// ── Layout ────────────────────────────────────────────────────────────────────

func blank() { fmt.Println() }

// hint prints a dim secondary note, indented to the message column.
func hint(s string) {
	fmt.Printf("%s  %s\n", strings.Repeat(" ", labelW), dim(s))
}

// cmdHint prints a suggested command, indented to the message column.
func cmdHint(s string) {
	fmt.Printf("%s  %s\n", strings.Repeat(" ", labelW), cyan(s))
}

// tableHeader prints a ruled table header. widths = visual widths for all cols
// except the last. The table is indented to the message column (labelW+2).
func tableHeader(ruler string, cols []string, widths []int) {
	indent := strings.Repeat(" ", labelW+2)
	fmt.Println(indent + ruler)
	fmt.Print(indent)
	for i, c := range cols {
		if i < len(cols)-1 {
			fmt.Print(padRight(bold(c), widths[i]))
			fmt.Print("  ")
		} else {
			fmt.Print(bold(c))
		}
	}
	fmt.Println()
	fmt.Println(indent + ruler)
}

// tableRow prints a table data row at the same indent as tableHeader.
func tableRow(marker string, cells ...string) {
	indent := strings.Repeat(" ", labelW+2)
	// marker replaces the first few chars of indent for the current-row arrow
	if marker != "" {
		indent = marker + indent[visibleLen(marker):]
	}
	fmt.Print(indent)
	for i, c := range cells {
		if i > 0 {
			fmt.Print("  ")
		}
		fmt.Print(c)
	}
	fmt.Println()
}

// sectionTitle prints a titled group heading — used before kv blocks.
func sectionTitle(s string) {
	fmt.Printf("\n%s  %s\n", strings.Repeat(" ", labelW), bold(s))
}

// ── Semantic helpers ──────────────────────────────────────────────────────────

func roleColor(role string) string {
	switch role {
	case "owner":
		return boldCyan(role)
	case "admin":
		return cyan(role)
	case "member":
		return green(role)
	default:
		return dim(role)
	}
}

func actionColor(action string) string {
	switch action {
	case "push":
		return green(action)
	case "pull":
		return cyan(action)
	case "invite":
		return yellow(action)
	case "revoke", "token_revoke":
		return red(action)
	case "token_create":
		return boldCyan(action)
	default:
		return dim(action)
	}
}
