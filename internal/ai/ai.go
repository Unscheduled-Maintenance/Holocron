// Package ai optionally rewrites a deterministic report into polished prose.
//
// The model never searches the archive. It receives only the entries the
// deterministic report already selected, each labelled with its ID, and is
// asked to cite those IDs. Citations are checked afterwards so every
// sentence stays traceable to source entries. Nothing here runs unless the
// person explicitly passes --ai, and API keys come only from the
// environment. See docs/adr/0006-ai-provider-boundary.md.
package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Unscheduled-Maintenance/Holocron/internal/config"
	"github.com/Unscheduled-Maintenance/Holocron/internal/report"
)

// Request is what a provider is asked to do.
type Request struct {
	System string
	Prompt string
}

// Response is a provider's answer.
type Response struct {
	Text  string
	Model string
}

// Provider is the boundary between Holocron and an AI service. Adding a
// provider means implementing this interface; nothing else depends on a
// particular vendor.
type Provider interface {
	// Name identifies the provider and model for disclosure, e.g. "anthropic (claude-opus-5-5)".
	Name() string
	Generate(ctx context.Context, req Request) (Response, error)
}

// ErrNotConfigured means no provider is set up.
var ErrNotConfigured = errors.New("AI is not configured: set ai.provider in the config (holocron config edit) and put the API key in an environment variable")

// ErrRefused means the provider declined the request.
var ErrRefused = errors.New("the AI provider declined to produce this report")

// New creates the configured provider. getenv is usually os.Getenv.
func New(cfg config.AIConfig, getenv func(string) string) (Provider, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", "none":
		return nil, ErrNotConfigured
	case "anthropic":
		envName := cfg.APIKeyEnv
		if envName == "" {
			envName = "ANTHROPIC_API_KEY"
		}
		key := getenv(envName)
		if key == "" {
			return nil, fmt.Errorf("ai.provider is anthropic but $%s is empty; export the API key in that variable (it is never read from the config file)", envName)
		}
		return newAnthropic(key, cfg.Model, cfg.BaseURL), nil
	}
	return nil, fmt.Errorf("unknown ai.provider %q (supported: anthropic)", cfg.Provider)
}

const systemPrompt = `You turn a person's own work-journal entries into a concise report they will share with colleagues.

Rules:
- Use only the facts in the supplied entries. Do not invent outcomes, numbers, names or dates.
- After every sentence or bullet that draws on an entry, cite it as [#ID] using the IDs given, e.g. "Enabled IAM Access Analyzer everywhere [#12]."
- Group related entries; merge near-duplicates instead of repeating them.
- Keep the report's sections and purpose. Prefer short bullets in plain, professional language.
- Output Markdown only, starting with a level-one heading. No preamble or closing remarks.`

// BuildRequest turns a deterministic report into a prompt containing only
// the entries that report selected.
func BuildRequest(rep report.Report) Request {
	var b strings.Builder
	fmt.Fprintf(&b, "Write the %q report titled %q.\n", rep.Kind, rep.Title)
	if rep.Range.Label != "" {
		fmt.Fprintf(&b, "Period: %s.\n", rep.Range.Label)
	}
	b.WriteString("\nThe deterministic draft below shows which entries were selected and how they are grouped:\n\n")
	_ = report.Markdown(&b, rep, report.RenderOptions{ShowIDs: true, TimeLayout: "15:04"})
	b.WriteString("\nFull text of each selected entry:\n\n")
	for _, id := range rep.SourceIDs() {
		e, ok := rep.Entries[id]
		if !ok {
			continue
		}
		meta := []string{e.OccurredAt.Format("2006-01-02")}
		if e.Project != "" {
			meta = append(meta, "project: "+e.Project)
		}
		if e.Type != "" {
			meta = append(meta, "type: "+string(e.Type))
		}
		if len(e.Tags) > 0 {
			meta = append(meta, "tags: "+strings.Join(e.Tags, ", "))
		}
		fmt.Fprintf(&b, "<entry id=\"#%d\" %s>\n%s\n</entry>\n", e.ID, strings.Join(meta, "; "), e.Body)
	}
	return Request{System: systemPrompt, Prompt: b.String()}
}

var citeRe = regexp.MustCompile(`\[#(\d+)\]`)

// Citations returns the cited IDs that are valid sources and any that are not.
func Citations(text string, rep report.Report) (cited, unknown []int64) {
	valid := map[int64]bool{}
	for _, id := range rep.SourceIDs() {
		valid[id] = true
	}
	seen := map[int64]bool{}
	for _, m := range citeRe.FindAllStringSubmatch(text, -1) {
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || seen[id] {
			continue
		}
		seen[id] = true
		if valid[id] {
			cited = append(cited, id)
		} else {
			unknown = append(unknown, id)
		}
	}
	sort.Slice(cited, func(i, j int) bool { return cited[i] < cited[j] })
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })
	return cited, unknown
}

// Provenance renders the appendix listing every source entry and whether
// the generated text cited it, so the reader can check the report.
func Provenance(text string, rep report.Report, providerName string) string {
	cited, unknown := Citations(text, rep)
	isCited := map[int64]bool{}
	for _, id := range cited {
		isCited[id] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n---\n\n## Sources\n\n_Generated by %s from %d entries selected by Holocron's %s report. Review before sharing._\n\n",
		providerName, len(rep.SourceIDs()), rep.Kind)
	for _, id := range rep.SourceIDs() {
		e := rep.Entries[id]
		mark := " "
		if isCited[id] {
			mark = "x"
		}
		fmt.Fprintf(&b, "- [%s] #%d %s\n", mark, id, e.Title())
	}
	if len(unknown) > 0 {
		parts := make([]string, len(unknown))
		for i, id := range unknown {
			parts[i] = fmt.Sprintf("#%d", id)
		}
		fmt.Fprintf(&b, "\n**Warning:** the text cites %s, which were not among the supplied entries. Treat those statements with suspicion.\n", strings.Join(parts, ", "))
	}
	return b.String()
}
