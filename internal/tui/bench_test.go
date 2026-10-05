package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/benchdata"
	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/report"
)

// benchModel opens an app over a large archive and returns a sized model
// showing every entry (the heaviest list the TUI keeps: 2000 rows).
func benchModel(b *testing.B, rangeKey string) Model {
	b.Helper()
	dir := b.TempDir()
	b.Setenv(config.EnvData, filepath.Join(dir, "data"))
	b.Setenv(config.EnvConfig, "")
	b.Setenv(config.EnvDB, "")
	cfg := filepath.Join(dir, "config.toml")
	if err := writeFile(cfg, ""); err != nil {
		b.Fatal(err)
	}
	a, err := app.Open(context.Background(), app.Options{ConfigPath: cfg, DBPath: filepath.Join(dir, "h.db"), UTC: true,
		Now: func() time.Time { return benchdata.Now }})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = a.Close() })
	if _, err := a.Store.AddEntries(context.Background(), benchdata.Entries(15000)); err != nil {
		b.Fatal(err)
	}
	m := New(context.Background(), a)
	m.setRangeKey(rangeKey)
	m = send(b, m, tea.WindowSizeMsg{Width: 140, Height: 45})
	return run(b, m, m.reload())
}

func BenchmarkTUILoadAll(b *testing.B) {
	m := benchModel(b, "all")
	for b.Loop() {
		m = run(b, m, m.reload())
	}
}

func BenchmarkTUIRenderAll(b *testing.B) {
	m := benchModel(b, "all")
	for b.Loop() {
		_ = m.render()
	}
}

func BenchmarkTUIRenderWeek(b *testing.B) {
	m := benchModel(b, "this-week")
	for b.Loop() {
		_ = m.render()
	}
}

func BenchmarkTUICursorMoveAll(b *testing.B) {
	m := benchModel(b, "all")
	i := 0
	for b.Loop() {
		k := "j"
		if i%2 == 1 {
			k = "k"
		}
		i++
		nm, _ := m.Update(key(k))
		m = nm.(Model)
		_ = m.render() // a key press is always followed by a frame
	}
}

func BenchmarkTUISearchKeystroke(b *testing.B) {
	m := benchModel(b, "all")
	m = send(b, m, key("/"))
	words := []string{"e", "ex", "exp", "expo", "expor", "export", "exporte", "exporter"}
	i := 0
	for b.Loop() {
		m.input.SetValue(words[i%len(words)][:len(words[i%len(words)])-1])
		r := []rune(words[i%len(words)])
		nm, cmd := m.Update(tea.KeyPressMsg{Code: r[len(r)-1], Text: string(r[len(r)-1])})
		m = run(b, nm.(Model), cmd)
		_ = m.render()
		i++
	}
}

func BenchmarkTUIPickerTags(b *testing.B) {
	m := benchModel(b, "all")
	for b.Loop() {
		_ = m.loadPicker(pickTag)()
	}
}

func BenchmarkTUIPickerProjects(b *testing.B) {
	m := benchModel(b, "all")
	for b.Loop() {
		_ = m.loadPicker(pickProject)()
	}
}

func benchReport(b *testing.B, k report.Kind, expr string) {
	m := benchModel(b, "this-week")
	r, err := m.app.Clock().Parse(expr)
	if err != nil {
		b.Fatal(err)
	}
	m.repKind = k
	for b.Loop() {
		msg := m.buildReport(&r)().(reportMsg)
		if msg.err != nil {
			b.Fatal(msg.err)
		}
		m.rep = msg.rep
		m.refreshReport()
	}
}

func BenchmarkReportStaffWeek(b *testing.B)    { benchReport(b, report.Staff, "this-week") }
func BenchmarkReportOneOnOne(b *testing.B)     { benchReport(b, report.OneOnOne, "14d") }
func BenchmarkReportQuarter(b *testing.B)      { benchReport(b, report.Quarter, "last-quarter") }
func BenchmarkReportWeekLastWeek(b *testing.B) { benchReport(b, report.Week, "last-week") }
func BenchmarkReportYear(b *testing.B)         { benchReport(b, report.Quarter, "last-year") }
