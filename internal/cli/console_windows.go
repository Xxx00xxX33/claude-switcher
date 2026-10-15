package cli

import (
	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/windows"
)

// enableVT turns on escape sequences for a console, which Windows PowerShell and cmd leave off.
// Fang does the same, but only once it runs: the background query comes earlier and would print
// as text. It reports whether the console now reads escape sequences.
func enableVT(f term.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
