package cmd

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// color codes
const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"

	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cRed    = "\033[31m"
	cCyan   = "\033[36m"
	cBlue   = "\033[34m"
	cWhite  = "\033[97m"
)

// isTTY returns true if stdout is a real terminal (not piped / CI redirect).
// Color codes are only emitted when this is true.
var isTTY = term.IsTerminal(int(os.Stdout.Fd()))

func colorize(code, s string) string {
	if !isTTY {
		return s
	}
	return code + s + cReset
}

// Semantic helpers — use these in commands, not raw codes.

func green(s string) string  { return colorize(cGreen, s) }
func yellow(s string) string { return colorize(cYellow, s) }
func red(s string) string    { return colorize(cRed, s) }
func cyan(s string) string   { return colorize(cCyan, s) }
func blue(s string) string   { return colorize(cBlue, s) }
func bold(s string) string   { return colorize(cBold, s) }
func dim(s string) string    { return colorize(cDim, s) }

// Prefixed status lines
func ok(s string) string   { return green("✅ " + s) }
func fail(s string) string { return red("❌ " + s) }
func info(s string) string { return cyan("→  " + s) }
func warn(s string) string { return yellow("⚠️  " + s) }
func spin(s string) string { return dim("⏳ " + s) }

// cfmt helpers — drop-in fmt.Print* replacements with color applied to the whole line.
func cprintf(format string, a ...any) { fmt.Printf(format, a...) }
