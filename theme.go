package main

import (
	"image/color"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
)

// Palette from logo.png: brand blue for emphasis, a navy-gray for secondary
// text. Body text keeps the terminal's own foreground; red is for errors
// only, orange for warnings, green for all-clear, the terminal's own yellow
// for passing notifications.
var (
	blue   = blueFor(true)
	red    = lipgloss.Color("1")
	yellow = lipgloss.Color("3")
	orange = lipgloss.Color("#e8930c")
	green  = lipgloss.Color("#2fb344")
	bold   = lipgloss.NewStyle().Bold(true)
	accent = lipgloss.NewStyle().Foreground(blue)
	bad    = lipgloss.NewStyle().Foreground(red)
	note   = lipgloss.NewStyle().Foreground(yellow)
	warn   = lipgloss.NewStyle().Foreground(orange)
	good   = lipgloss.NewStyle().Foreground(green)
	muted  = lipgloss.NewStyle().Foreground(gray(true))
	isDark = true
)

// blueFor is the logo blue on light backgrounds and a lighter tint of it on
// dark ones, where the original is too dim.
func blueFor(dark bool) color.Color {
	return lipgloss.LightDark(dark)(lipgloss.Color("#026bdd"), lipgloss.Color("#6cb0ff"))
}

func gray(dark bool) color.Color {
	return lipgloss.LightDark(dark)(lipgloss.Color("#6b7684"), lipgloss.Color("#8a97a8"))
}

// setTheme adapts the background-dependent styles once the terminal has
// reported whether its background is dark.
func setTheme(dark bool) {
	isDark = dark
	blue = blueFor(dark)
	accent = lipgloss.NewStyle().Foreground(blue)
	muted = lipgloss.NewStyle().Foreground(gray(dark))
}

// delegate renders single-line list items, the selected one blue with a caret.
func delegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	d.Styles.NormalTitle = lipgloss.NewStyle().Padding(0, 0, 0, 2)
	d.Styles.SelectedTitle = lipgloss.NewStyle().Foreground(blue).SetString("❯")
	d.Styles.DimmedTitle = d.Styles.NormalTitle.Foreground(gray(isDark))
	return d
}
