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
	cPurple       = "\033[95m"
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

func green(s string) string     { return colorize(cBrightGreen, s) }
func yellow(s string) string    { return colorize(cBrightYellow, s) }
func red(s string) string       { return colorize(cBrightRed, s) }
func cyan(s string) string      { return colorize(cBrightCyan, s) }
func blue(s string) string      { return colorize(cBlue, s) }
func magenta(s string) string   { return colorize(cMagenta, s) }
func purple(s string) string    { return colorize(cBrightPurple, s) }
func bold(s string) string      { return colorize(cBold, s) }
func dim(s string) string       { return colorize(cDim, s) }
func italic(s string) string    { return colorize(cItalic, s) }
func boldCyan(s string) string  { return colorize(cBold+cBrightCyan, s) }
func boldGreen(s string) string { return colorize(cBold+cBrightGreen, s) }
func boldRed(s string) string   { return colorize(cBold+cBrightRed, s) }
func boldPurple(s string) string { return colorize(cBold+cBrightPurple, s) }

// ── Status line system ────────────────────────────────────────────────────────
//
// Design: bun / pnpm style.
// A short right-aligned verb in a fixed 8-char column, two spaces, then message.
// Everything — status lines, kv pairs, table headers — shares one left margin.
//
//   Output examples:
//
//     done  my-app/dev → v7
//     warn  .env already exists
//      enc  my-app/dev — 12 secrets
//     info  signature verified (alice, ed25519)
//
// The verb column is 6 visible chars, right-aligned with a leading space:
//   " " + rightPad(verb, 6) + "  " + message
// Total prefix before message: 1 + 6 + 2 = 9 chars.
//
// Tables and kv blocks use the same 9-char left margin so everything lines up.

const (
	verbW  = 6 // visible width of the verb/label column
	margin = 2 // spaces between verb and message
)

// msgIndent is the full left margin: 1 leading space + verbW + margin
const msgIndent = 1 + verbW + margin // = 9

func msgPad() string { return strings.Repeat(" ", msgIndent) }

// verb builds a right-aligned verb label.
func verb(s string, colorFn func(string) string) string {
	pad := verbW - len(s)
	if pad < 0 {
		pad = 0
	}
	return " " + strings.Repeat(" ", pad) + colorFn(s)
}

// ── Status constructors ───────────────────────────────────────────────────────

func ok(s string) string {
	return verb("done", boldGreen) + "  " + s
}

func step(s string) string {
	return verb("·", dim) + "  " + dim(s)
}

func spin(s string) string { return step(s) }

func fail(s string) string {
	return verb("error", boldRed) + "  " + s
}

func warn(s string) string {
	return verb("warn", yellow) + "  " + s
}

func info(s string) string {
	return verb("info", cyan) + "  " + s
}

// prog prints a named operation step — verb is the action word (e.g. "enc"),
// msg is the detail. Verb is bold cyan like bun's package name column.
func prog(v, msg string) string {
	// Truncate verb to verbW chars if needed
	if len(v) > verbW {
		v = v[:verbW]
	}
	return verb(v, boldCyan) + "  " + msg
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
// Labels right-aligned to verbW, same column as status verbs above.
// Value starts at msgIndent — same as status messages.

func kv(lbl, value string) {
	fmt.Printf("%s  %s\n", verb(lbl, bold), value)
}
func kvGreen(lbl, value string) {
	fmt.Printf("%s  %s\n", verb(lbl, bold), green(value))
}
func kvCyan(lbl, value string) {
	fmt.Printf("%s  %s\n", verb(lbl, bold), cyan(value))
}
func kvDim(lbl, value string) {
	fmt.Printf("%s  %s\n", verb(lbl, bold), dim(value))
}
func kvRed(lbl, value string) {
	fmt.Printf("%s  %s\n", verb(lbl, bold), red(value))
}

// ── Layout primitives ─────────────────────────────────────────────────────────

func blank() { fmt.Println() }

// hint prints a dim secondary note, indented to the message column.
func hint(s string) {
	fmt.Printf("%s%s\n", msgPad(), dim(s))
}

// cmdHint prints a suggested command in cyan, indented to the message column.
func cmdHint(s string) {
	fmt.Printf("%s%s\n", msgPad(), cyan(s))
}

// tableHeader prints a ruled table header at msgIndent.
func tableHeader(ruler string, cols []string, widths []int) {
	pad := msgPad()
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

// tableRow prints a data row at msgIndent.
// marker replaces the first few visible chars of the indent (e.g. for highlighting).
func tableRow(marker string, cells ...string) {
	if marker == "" {
		fmt.Print(msgPad())
	} else {
		// marker fills msgIndent chars visually
		mLen := visibleLen(marker)
		extra := msgIndent - mLen
		if extra < 0 {
			extra = 0
		}
		fmt.Print(marker + strings.Repeat(" ", extra))
	}
	for i, c := range cells {
		if i > 0 {
			fmt.Print("  ")
		}
		fmt.Print(c)
	}
	fmt.Println()
}

// sectionTitle prints a bold section label at the message column.
func sectionTitle(s string) {
	fmt.Printf("\n%s%s\n", msgPad(), bold(s))
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

// ── Loading spinner ─────────────────────────────────────────────────────────

var spinnerChars = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
var spinnerIndex = 0

func spinner() string {
	s := spinnerChars[spinnerIndex]
	spinnerIndex = (spinnerIndex + 1) % len(spinnerChars)
	return s
}

func resetSpinner() {
	spinnerIndex = 0
}

// ── Enhanced status with loading ─────────────────────────────────────────────

func loading(msg string) string {
	return verb("loading", boldCyan) + "  " + dim(msg) + " " + spinner()
}
