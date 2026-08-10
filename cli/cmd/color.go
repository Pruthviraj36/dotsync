package cmd

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ── ANSI codes ────────────────────────────────────────────────────────────────

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cItalic = "\033[3m"

	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cRed    = "\033[31m"
	cCyan   = "\033[36m"
	cBlue   = "\033[34m"
	cWhite  = "\033[97m"

	// Bright variants — more vivid on dark terminals
	cBrightGreen = "\033[92m"
	cBrightCyan  = "\033[96m"
	cBrightRed   = "\033[91m"
)

// isTTY — color and box-drawing only when stdout is a real terminal.
var isTTY = term.IsTerminal(int(os.Stdout.Fd()))

func colorize(code, s string) string {
	if !isTTY {
		return s
	}
	return code + s + cReset
}

// ── Semantic color helpers ────────────────────────────────────────────────────

func green(s string) string       { return colorize(cBrightGreen, s) }
func yellow(s string) string      { return colorize(cYellow, s) }
func red(s string) string         { return colorize(cBrightRed, s) }
func cyan(s string) string        { return colorize(cBrightCyan, s) }
func blue(s string) string        { return colorize(cBlue, s) }
func bold(s string) string        { return colorize(cBold, s) }
func dim(s string)  string        { return colorize(cDim, s) }
func italic(s string) string      { return colorize(cItalic, s) }
func boldCyan(s string) string    { return colorize(cBold+cBrightCyan, s) }
func boldGreen(s string) string   { return colorize(cBold+cBrightGreen, s) }

// ── Status line prefixes ──────────────────────────────────────────────────────
// All prefixes are exactly 9 chars wide (including trailing spaces) so that
// the message text always starts at the same column — like Doppler / Vercel CLI.
//
//   success  your message here
//   error    your message here
//   warning  your message here
//   info     your message here
//   waiting  your message here

func ok(s string) string   { return boldGreen("success") + "  " + s }
func fail(s string) string { return red("error") + "    " + s }
func warn(s string) string { return yellow("warning") + "  " + s }
func info(s string) string { return cyan("info") + "     " + s }
func spin(s string) string { return dim("waiting") + "  " + s }

// ── Terminal width ────────────────────────────────────────────────────────────

// termWidth returns the current terminal column width, clamped to [60, 120].
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

// rule returns a horizontal rule of ─ at terminal width.
func rule() string { return strings.Repeat("─", termWidth()-2) }

// ruleN returns a horizontal rule of exactly n chars.
func ruleN(n int) string { return strings.Repeat("─", n) }

// ── Structured output helpers ─────────────────────────────────────────────────

// header prints a titled section header with a rule underneath.
//
//	DotSync — push
//	──────────────────────────────────────────────────────────
func header(title string) {
	fmt.Println()
	fmt.Println(bold(title))
	fmt.Println(ruleN(len(title) + 2))
}

// section prints a labelled block:
//
//	  Project   my-app
//	  Env       production
func kv(label, value string) {
	fmt.Printf("  %-12s%s\n", bold(label), value)
}

// kvGreen prints a kv pair with the value in green.
func kvGreen(label, value string) {
	fmt.Printf("  %-12s%s\n", bold(label), green(value))
}

// kvCyan prints a kv pair with the value in cyan.
func kvCyan(label, value string) {
	fmt.Printf("  %-12s%s\n", bold(label), cyan(value))
}

// kvDim prints a kv pair with a dim value.
func kvDim(label, value string) {
	fmt.Printf("  %-12s%s\n", bold(label), dim(value))
}

// blank prints a blank line.
func blank() { fmt.Println() }

// hint prints a dim hint line, indented.
func hint(s string) { fmt.Printf("  %s\n", dim(s)) }

// cmd prints an example command in cyan, indented.
func cmdHint(s string) { fmt.Printf("  %s\n", cyan(s)) }

// tableHeader prints a ruled table with bold column headers.
// widths is the padded width for each column (last column has no padding).
func tableHeader(rule string, cols []string, widths []int) {
	fmt.Println("  " + rule)
	fmt.Print("  ")
	for i, col := range cols {
		if i < len(cols)-1 {
			fmt.Printf(bold("%-*s")+"  ", widths[i], col)
		} else {
			fmt.Printf(bold("%s"), col)
		}
	}
	fmt.Println()
	fmt.Println("  " + rule)
}

// roleColor applies a color to a team role string.
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

// actionColor applies a color to an audit action string.
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
