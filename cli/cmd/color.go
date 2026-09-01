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
	cBrightGreen  = "\033[92m"
	cBrightCyan   = "\033[96m"
	cBrightRed    = "\033[91m"
	cBrightYellow = "\033[93m"
	cBrightPurple = "\033[95m"
)

var isTTY = term.IsTerminal(int(os.Stdout.Fd()))

func colorize(code, s string) string {
	if !isTTY {
		return s
	}
	return code + s + cReset
}

// ── Color primitives ──────────────────────────────────────────────────────────

func green(s string) string      { return colorize(cBrightGreen, s) }
func yellow(s string) string     { return colorize(cBrightYellow, s) }
func red(s string) string        { return colorize(cBrightRed, s) }
func cyan(s string) string       { return colorize(cBrightCyan, s) }
func blue(s string) string       { return colorize(cBlue, s) }
func magenta(s string) string    { return colorize(cMagenta, s) }
func purple(s string) string     { return colorize(cBrightPurple, s) }
func bold(s string) string       { return colorize(cBold, s) }
func dim(s string) string        { return colorize(cDim, s) }
func italic(s string) string     { return colorize(cItalic, s) }
func boldCyan(s string) string   { return colorize(cBold+cBrightCyan, s) }
func boldGreen(s string) string  { return colorize(cBold+cBrightGreen, s) }
func boldRed(s string) string    { return colorize(cBold+cBrightRed, s) }
func boldPurple(s string) string { return colorize(cBold+cBrightPurple, s) }

// ── Layout system ─────────────────────────────────────────────────────────────
//
// Two visual tiers:
//
// TIER 1 — Progress lines (while something is happening)
//   Right-aligned verb in 12-char column, 2-space gap, message.
//   Feels like cargo/bun. Used during active operations.
//
//       Encrypting  gods-eye/dev — 18 secrets
//         Uploading  1.4 KB
//             done  gods-eye/dev → v1
//
// TIER 2 — Summary/info blocks (after completion, or for status/version)
//   Simple 2-space indent, dim label, two spaces, value.
//   Labels are left-aligned in their own narrow column.
//   Feels like git status / gh cli.
//
//   project   gods-eye
//   env       dev
//   version   v1
//   secrets   18 encrypted
//
// This separation makes it clear what's "happening" vs "here's the result".

const (
	// Tier 1: progress verb column
	verbCol   = 12
	verbGap   = 2

	// Tier 2: summary label column
	lblCol    = 10 // left-aligned, no right-alignment
)

// ── Tier 1: progress lines ────────────────────────────────────────────────────

func progLine(verb, colorFn func(string) string, text string) string {
	// right-align verb in verbCol chars
	vLen := len(verb("")) // len of colorized empty string = just ansi codes, not useful
	_ = vLen
	rawVerb := verb
	_ = rawVerb
	pad := verbCol - len(text)
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + colorFn(text) + strings.Repeat(" ", verbGap)
}

// prog prints a named operation in progress.
// verb: the action word (e.g. "Encrypting") — bold cyan, right-aligned.
// msg: the detail — printed as-is.
func prog(verb, msg string) string {
	pad := verbCol - len(verb)
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + boldCyan(verb) + strings.Repeat(" ", verbGap) + msg
}

// ok prints a completion line.
func ok(msg string) string {
	pad := verbCol - len("done")
	return strings.Repeat(" ", pad) + boldGreen("done") + strings.Repeat(" ", verbGap) + msg
}

// step prints an anonymous in-progress line.
func step(msg string) string {
	pad := verbCol - 1
	return strings.Repeat(" ", pad) + dim("·") + strings.Repeat(" ", verbGap) + dim(msg)
}

func spin(msg string) string { return step(msg) }

func fail(msg string) string {
	pad := verbCol - len("error")
	return strings.Repeat(" ", pad) + boldRed("error") + strings.Repeat(" ", verbGap) + msg
}

func warn(msg string) string {
	pad := verbCol - len("warn")
	return strings.Repeat(" ", pad) + yellow("warn") + strings.Repeat(" ", verbGap) + msg
}

func info(msg string) string {
	pad := verbCol - len("info")
	return strings.Repeat(" ", pad) + cyan("info") + strings.Repeat(" ", verbGap) + msg
}

// ── Tier 2: summary/info blocks ───────────────────────────────────────────────
// Labels are dim, left-aligned, value follows after padding to lblCol.
// Total left indent: 2 spaces.

func kv(label, value string) {
	fmt.Printf("  %s  %s\n", padRight(dim(label), lblCol), value)
}

func kvGreen(label, value string) {
	fmt.Printf("  %s  %s\n", padRight(dim(label), lblCol), green(value))
}

func kvCyan(label, value string) {
	fmt.Printf("  %s  %s\n", padRight(dim(label), lblCol), cyan(value))
}

func kvDim(label, value string) {
	fmt.Printf("  %s  %s\n", padRight(dim(label), lblCol), dim(value))
}

func kvRed(label, value string) {
	fmt.Printf("  %s  %s\n", padRight(dim(label), lblCol), red(value))
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
	if w > 120 {
		return 120
	}
	return w
}

func ruleN(n int) string {
	if n < 1 {
		return ""
	}
	return dim(strings.Repeat("─", n))
}

// msgPad returns leading whitespace that aligns with the end of the verb column.
// Use for hints and cmds that sit below a prog/ok line.
func msgPad() string {
	return strings.Repeat(" ", verbCol+verbGap)
}

// ── ANSI-aware measurement ────────────────────────────────────────────────────

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

// ── Layout helpers ────────────────────────────────────────────────────────────

func blank() { fmt.Println() }

func hint(s string) { fmt.Printf("%s%s\n", msgPad(), dim(s)) }

func cmdHint(s string) { fmt.Printf("%s%s\n", msgPad(), cyan(s)) }

// tableHeader prints a ruled table header indented to the summary block indent.
func tableHeader(ruler string, cols []string, widths []int) {
	pad := "  "
	fmt.Println(pad + ruler)
	fmt.Print(pad)
	for i, c := range cols {
		if i < len(cols)-1 {
			fmt.Print(padRight(bold(c), widths[i]))
			fmt.Print("  ")
		} else {
			fmt.Print(bold(c))
		}
	}
	fmt.Println()
	fmt.Println(pad + ruler)
}

// tableRow prints a data row.
func tableRow(marker string, cells ...string) {
	pad := "  "
	if marker != "" {
		mLen := visibleLen(marker)
		extra := 2 - mLen
		if extra < 0 {
			extra = 0
		}
		pad = marker + strings.Repeat(" ", extra)
	}
	fmt.Print(pad)
	for i, c := range cells {
		if i > 0 {
			fmt.Print("  ")
		}
		fmt.Print(c)
	}
	fmt.Println()
}

func sectionTitle(s string) {
	fmt.Printf("\n  %s\n", bold(s))
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
