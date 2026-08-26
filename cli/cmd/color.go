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
// Design: cargo / bun inspired — a right-aligned label column, two spaces gap,
// then the message. All output shares one left axis.
//
// Label column: 12 visible chars, right-aligned.
// Full left margin before message: 12 + 2 = 14 chars.
//
//   Encrypting  gods-eye/dev — 18 secrets
//       done  gods-eye/dev → v1
//       warn  .env already exists
//      error  file not found: .env
//
// Labels longer than 12 chars are printed as-is (no truncation).
// The column is wide enough for all real verbs: "Encrypting" (10),
// "Decrypting" (10), "Uploading" (8), "Downloading" (11), "Connecting" (10).

const (
	labelW = 12 // visible width of the right-aligned label column
	gapW   = 2  // spaces between label and message
	// msgIndent = labelW + gapW = 14
	msgIndent = labelW + gapW
)

func msgPad() string { return strings.Repeat(" ", msgIndent) }

// lbl right-aligns text in labelW chars, applies colorFn, returns the full
// label field. Never truncates — if text > labelW, alignment breaks but
// text is preserved (correctness > aesthetics).
func lbl(text string, colorFn func(string) string) string {
	pad := labelW - len(text)
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + colorFn(text)
}

// ── Status constructors ───────────────────────────────────────────────────────

// ok — completed successfully. Lowercase, right-aligned, bold green.
func ok(msg string) string {
	return lbl("done", boldGreen) + strings.Repeat(" ", gapW) + msg
}

// prog — named operation in progress. verb is the action (e.g. "Encrypting"),
// msg is the detail. Verb is right-aligned bold cyan.
func prog(verb, msg string) string {
	return lbl(verb, boldCyan) + strings.Repeat(" ", gapW) + msg
}

// step — anonymous in-progress line. Dim, no verb noise.
func step(msg string) string {
	return lbl("·", dim) + strings.Repeat(" ", gapW) + dim(msg)
}

// spin is an alias for step.
func spin(msg string) string { return step(msg) }

// fail — hard error.
func fail(msg string) string {
	return lbl("error", boldRed) + strings.Repeat(" ", gapW) + msg
}

// warn — non-fatal warning.
func warn(msg string) string {
	return lbl("warn", yellow) + strings.Repeat(" ", gapW) + msg
}

// info — informational.
func info(msg string) string {
	return lbl("info", cyan) + strings.Repeat(" ", gapW) + msg
}

// ── Terminal width ────────────────────────────────────────────────────────────

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

// ── Key-value pairs ───────────────────────────────────────────────────────────
// Labels right-aligned to labelW — same column as status verbs.
// All values start at msgIndent.

func kv(label, value string)      { fmt.Printf("%s  %s\n", lbl(label, bold), value) }
func kvGreen(label, value string) { fmt.Printf("%s  %s\n", lbl(label, bold), green(value)) }
func kvCyan(label, value string)  { fmt.Printf("%s  %s\n", lbl(label, bold), cyan(value)) }
func kvDim(label, value string)   { fmt.Printf("%s  %s\n", lbl(label, bold), dim(value)) }
func kvRed(label, value string)   { fmt.Printf("%s  %s\n", lbl(label, bold), red(value)) }

// ── Layout helpers ────────────────────────────────────────────────────────────

func blank() { fmt.Println() }

// hint prints a dim note at the message column.
func hint(s string) { fmt.Printf("%s%s\n", msgPad(), dim(s)) }

// cmdHint prints a suggested command in cyan at the message column.
func cmdHint(s string) { fmt.Printf("%s%s\n", msgPad(), cyan(s)) }

// tableHeader prints a ruled table header. widths are visual widths for all
// columns except the last.
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
func tableRow(marker string, cells ...string) {
	pad := msgPad()
	if marker != "" {
		mLen := visibleLen(marker)
		extra := msgIndent - mLen
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

// sectionTitle prints a bold heading at the message column.
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
