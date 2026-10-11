package cmd

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ── ANSI codes ────────────────────────────────────────────────────────────────

const (
	cReset   = "\033[0m"
	cBold    = "\033[1m"
	cDim     = "\033[2m"
	cItalic  = "\033[3m"
	cGreen   = "\033[32m"
	cYellow  = "\033[33m"
	cRed     = "\033[31m"
	cCyan    = "\033[36m"
	cBlue    = "\033[34m"
	cMagenta = "\033[35m"
)

var isTTY = term.IsTerminal(int(os.Stdout.Fd()))

func colorize(code, s string) string {
	if !isTTY || os.Getenv("NO_COLOR") != "" {
		return s
	}
	return code + s + cReset
}

// ── Color primitives (clean & minimal) ────────────────────────────────────────

func green(s string) string      { return colorize(cGreen, s) }
func yellow(s string) string     { return colorize(cYellow, s) }
func red(s string) string        { return colorize(cRed, s) }
func cyan(s string) string       { return colorize(cCyan, s) }
func blue(s string) string       { return colorize(cBlue, s) }
func magenta(s string) string    { return colorize(cMagenta, s) }
func purple(s string) string     { return colorize(cMagenta, s) }
func bold(s string) string       { return colorize(cBold, s) }
func dim(s string) string        { return colorize(cDim, s) }
func italic(s string) string     { return colorize(cItalic, s) }
func boldCyan(s string) string   { return colorize(cBold+cCyan, s) }
func boldGreen(s string) string  { return colorize(cBold+cGreen, s) }
func boldRed(s string) string    { return colorize(cBold+cRed, s) }
func boldPurple(s string) string { return colorize(cBold+cMagenta, s) }

// ── Pure DNF / Fedora Layout System ───────────────────────────────────────────
//
// Minimal, structured, monospace, user-friendly terminal presentation:
//
// 1. Transaction Banners (80-col rules)
//    ================================================================================
//     Target: gods-eye/production
//    ================================================================================
//
// 2. Numbered step progress (Running transaction)
//    Running transaction:
//      [1/3] Encrypting secrets (18 items)... done.
//      [2/3] Verifying and attaching ed25519 signature... done.
//      [3/3] Uploading ciphertext (1.4 KB)... done.
//
//    Complete!
//
// 3. Labeled records for inspectable data (status, diff, history, team, tokens)
//      Version: v3
//      Author:  @alice
//      Age:     just now

func banner(title string) {
	fmt.Printf("\n%s\n\n", bold(title))
}

func sectionTitle(s string) {
	banner(s)
}

func txRunning(label string) {
	if label == "" {
		label = "transaction"
	}
	fmt.Printf("Running %s:\n", label)
}

func txStep(curr, total int, msg string) {
	fmt.Printf("  [%d/%d] %s... %s\n", curr, total, msg, green("done."))
}

func txStepCustom(curr, total int, msg, status string) {
	fmt.Printf("  [%d/%d] %s... %s\n", curr, total, msg, status)
}

func transferProgress(label string, total, completed int) {
	if total < 1 {
		total = 1
	}
	if completed < 0 {
		completed = 0
	}
	if completed > total {
		completed = total
	}

	const width = 28
	filled := width * completed / total
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	percent := completed * 100 / total
	line := fmt.Sprintf("  %-11s [%s] %3d%%", label, bar, percent)
	if isTTY {
		fmt.Printf("\r%s", line)
		if completed == total {
			fmt.Println()
		}
		return
	}
	fmt.Println(line)
}

func complete(msg string) {
	fmt.Println()
	if msg != "" {
		fmt.Printf("%s %s\n", boldGreen("Complete!"), msg)
	} else {
		fmt.Println(boldGreen("Complete!"))
	}
}

// ── Standard status indicators ────────────────────────────────────────────────

func ok(msg string) string {
	return fmt.Sprintf("[  %s  ] %s", green("OK"), msg)
}

func info(msg string) string {
	return fmt.Sprintf("[ %s ] %s", dim("INFO"), msg)
}

func warn(msg string) string {
	return fmt.Sprintf("[ %s ] %s", yellow("WARN"), msg)
}

func fail(msg string) string {
	return fmt.Sprintf("[ %s ] %s", red("FAIL"), msg)
}

func step(msg string) string {
	return fmt.Sprintf("  * %s", msg)
}

func spin(msg string) string {
	return step(msg)
}

func prog(verb, msg string) string {
	return fmt.Sprintf("  * %s: %s", bold(verb), msg)
}

// ── Key-Value & Summary blocks ────────────────────────────────────────────────

func kv(label, value string) {
	fmt.Printf(" %-15s: %s\n", dim(label), value)
}

func kvGreen(label, value string) {
	fmt.Printf(" %-15s: %s\n", dim(label), green(value))
}

func kvCyan(label, value string) {
	fmt.Printf(" %-15s: %s\n", dim(label), value)
}

func kvDim(label, value string) {
	fmt.Printf(" %-15s: %s\n", dim(label), dim(value))
}

func kvRed(label, value string) {
	fmt.Printf(" %-15s: %s\n", dim(label), red(value))
}

func transactionSummary(title string, items [][2]string) {
	fmt.Println()
	if title == "" {
		title = "Transaction Summary"
	}
	fmt.Println(title)
	for _, item := range items {
		fmt.Printf(" %-15s: %s\n", dim(item[0]), item[1])
	}
}

// ── Labeled record formatting ────────────────────────────────────────────────

func table(headers []string, rows [][]string) {
	if len(headers) == 0 || len(rows) == 0 {
		return
	}

	for rowIndex, row := range rows {
		if rowIndex > 0 {
			fmt.Println()
		}
		for i, header := range headers {
			if i >= len(row) || row[i] == "" {
				continue
			}
			fmt.Printf("  %s: %s\n", dim(header), row[i])
		}
	}
	fmt.Println()
}

func msgPad() string {
	return "  "
}

func blank() {
	fmt.Println()
}

func hint(s string) {
	fmt.Printf("  Note: %s\n", dim(s))
}

func cmdHint(s string) {
	fmt.Printf("  Run:  %s\n", s)
}

func item(primary, detail string) {
	if detail == "" {
		fmt.Printf("  * %s\n", primary)
		return
	}
	fmt.Printf("  * %s\n    %s\n", primary, dim(detail))
}

// ── Semantic helpers ──────────────────────────────────────────────────────────

func roleColor(role string) string {
	switch role {
	case "owner":
		return bold(role)
	case "admin":
		return bold(role)
	case "member":
		return role
	default:
		return dim(role)
	}
}

func actionColor(action string) string {
	switch action {
	case "push":
		return green(action)
	case "pull":
		return bold(action)
	case "invite":
		return yellow(action)
	case "revoke", "token_revoke":
		return red(action)
	case "token_create":
		return bold(action)
	default:
		return dim(action)
	}
}
