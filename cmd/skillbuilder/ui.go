package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"

	"github.com/sidharthp/skillbuilder/internal/api"
	"github.com/sidharthp/skillbuilder/internal/installer"
)

// Colours are adaptive so the output stays legible on light terminals, and
// deliberately sparse: status carries the colour, prose does not.
var (
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "241"})
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "42"})
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "130", Dark: "214"})
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
	nameStyle = lipgloss.NewStyle().Bold(true)
)

// errCancelled is returned when the user backs out of a prompt. It prints
// nothing, because they already know what they did.
var errCancelled = errors.New("cancelled")

// termWidth returns a usable line width, clamped so output neither wraps on a
// narrow terminal nor sprawls across a maximized one.
func termWidth() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 0 {
		return 80
	}
	switch {
	case w < 40:
		return 40
	case w > 120:
		return 120
	}
	return w
}

// interactive reports whether a prompt can be shown. Both ends matter: a piped
// stdin cannot answer, and a piped stdout should not be filled with a TUI.
func interactive() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if max <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimRight(string(r[:max-1]), " ,.;:—-") + "…"
}

func pad(s string, w int) string {
	if n := w - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// nameColumn sizes the first column to the longest name, within reason.
func nameColumn(names []string) int {
	w := 0
	for _, n := range names {
		if len(n) > w {
			w = len(n)
		}
	}
	switch {
	case w < 12:
		return 12
	case w > 32:
		return 32
	}
	return w
}

// printSkills renders a name/description table sized to the terminal.
func printSkills(w io.Writer, skills []api.Skill, note func(api.Skill) string) {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	col := nameColumn(names)
	width := termWidth()

	for _, s := range skills {
		prefix := "  "
		if note != nil {
			if n := note(s); n != "" {
				prefix = n
			}
		}
		line := prefix + pad(s.Name, col)
		if desc := truncate(s.Description, width-lipgloss.Width(line)-3); desc != "" {
			line += "  " + dimStyle.Render(desc)
		}
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
}

// statusMark returns the leading glyph and label style for an install result.
func statusMark(s installer.Status) (string, lipgloss.Style) {
	switch s {
	case installer.StatusInstalled, installer.StatusUpdated:
		return okStyle.Render("✓"), okStyle
	case installer.StatusSkipped:
		return warnStyle.Render("!"), warnStyle
	default:
		return dimStyle.Render("·"), dimStyle
	}
}

// printResults renders one line per skill plus a summary.
func printResults(w io.Writer, results []installer.Result, dir string, dryRun bool) {
	if len(results) == 0 {
		fmt.Fprintln(w, dimStyle.Render("nothing to install"))
		return
	}
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}
	col := nameColumn(names)
	width := termWidth()

	for _, r := range results {
		mark, style := statusMark(r.Status)
		line := "  " + mark + " " + pad(r.Name, col) + "  " + style.Render(string(r.Status))
		if r.Reason != "" {
			if reason := truncate(r.Reason, width-lipgloss.Width(line)-4); reason != "" {
				line += dimStyle.Render(" — " + reason)
			}
		}
		fmt.Fprintln(w, line)
	}

	counts := installer.Counts(results)
	var parts []string
	for _, s := range []installer.Status{installer.StatusInstalled, installer.StatusUpdated, installer.StatusUnchanged, installer.StatusSkipped} {
		if n := counts[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	summary := strings.Join(parts, ", ")
	if dryRun {
		fmt.Fprintln(w, dimStyle.Render("dry run — "+summary+"; nothing was written"))
		return
	}
	fmt.Fprintln(w, dimStyle.Render(summary+" in "+dir))
}

// selectSkills shows the multi-select picker. preselect seeds every option as
// chosen, which is what `init` wants: a bundle is a curated set, so opting out
// should be the exception.
func selectSkills(title string, skills []api.Skill, preselect bool) ([]string, error) {
	width := termWidth()
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	col := nameColumn(names)

	opts := make([]huh.Option[string], 0, len(skills))
	for _, s := range skills {
		label := pad(s.Name, col)
		if desc := truncate(s.Description, width-col-14); desc != "" {
			label += "  " + desc
		}
		opts = append(opts, huh.NewOption(label, s.Name).Selected(preselect))
	}

	height := len(opts) + 4
	if height > 20 {
		height = 20
	}

	var chosen []string
	err := huh.NewMultiSelect[string]().
		Title(title).
		Description("space toggles · ctrl+a all · / filters · enter confirms").
		Options(opts...).
		Filterable(true).
		Height(height).
		Value(&chosen).
		Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return nil, errCancelled
	}
	if err != nil {
		return nil, fmt.Errorf("skill picker: %w", err)
	}
	sort.Strings(chosen)
	return chosen, nil
}
