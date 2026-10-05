package tui

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestRealProgramWithScriptedKeys runs the actual Bubble Tea program with
// raw terminal input, exercising key decoding, the event loop and rendering.
func TestRealProgramWithScriptedKeys(t *testing.T) {
	a := newTestApp(t)
	in, w := io.Pipe()
	var out bytes.Buffer
	p := tea.NewProgram(New(context.Background(), a),
		tea.WithInput(in), tea.WithOutput(&out), tea.WithWindowSize(100, 30), tea.WithoutSignals())

	done := make(chan tea.Model, 1)
	errc := make(chan error, 1)
	go func() {
		m, err := p.Run()
		errc <- err
		done <- m
	}()
	typeKeys := func(s string) {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Errorf("write: %v", err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	typeKeys("j")
	typeKeys("\x1b[B") // down arrow
	typeKeys("\r")     // open detail
	typeKeys("\x1b")   // esc: back to list
	time.Sleep(200 * time.Millisecond)
	typeKeys("a")
	typeKeys("Scripted entry +AWS #script")
	typeKeys("\r")
	typeKeys("q")

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("program error: %v", err)
		}
	case <-time.After(10 * time.Second):
		p.Kill()
		t.Fatal("program did not quit on q")
	}
	final := (<-done).(Model)
	if final.screen != screenList {
		t.Errorf("expected to end on the list, got screen %d", final.screen)
	}
	es, err := a.Store.Find(context.Background(), queryText("scripted"))
	if err != nil || len(es) != 1 || es[0].Project != "AWS" || !es[0].HasTag("script") {
		t.Fatalf("scripted capture not saved correctly: %+v %v", es, err)
	}
	if out.Len() == 0 {
		t.Error("nothing was rendered")
	}
}
