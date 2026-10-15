//go:build !windows

package cli

import "github.com/charmbracelet/x/term"

// enableVT has nothing to do outside Windows: terminals read escape sequences.
func enableVT(term.File) bool { return true }
