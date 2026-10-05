package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// DefaultAnthropicModel is used when ai.model is empty.
const DefaultAnthropicModel = "claude-opus-5-5"

type anthropicProvider struct {
	client anthropic.Client
	model  string
}

func newAnthropic(key, model, baseURL string) *anthropicProvider {
	opts := []option.RequestOption{option.WithAPIKey(key)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &anthropicProvider{client: anthropic.NewClient(opts...), model: model}
}

func (p *anthropicProvider) Name() string { return "anthropic (" + p.model + ")" }

func (p *anthropicProvider) Generate(ctx context.Context, req Request) (Response, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     p.model,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: req.System}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(req.Prompt)),
		},
		// Rewriting a short report is routine work: medium effort is ample.
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
		// If a safety classifier declines, let the API re-serve the request
		// on an appropriate fallback model instead of failing outright.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
	}
	msg, err := p.client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 401, 403:
				return Response{}, fmt.Errorf("the Anthropic API rejected the API key (HTTP %d)", apiErr.StatusCode)
			case 429:
				return Response{}, fmt.Errorf("the Anthropic API is rate limiting requests; try again shortly")
			default:
				return Response{}, fmt.Errorf("the Anthropic API returned HTTP %d", apiErr.StatusCode)
			}
		}
		return Response{}, fmt.Errorf("contacting the Anthropic API: %w", err)
	}
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		return Response{}, ErrRefused
	}
	var b strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return Response{}, errors.New("the AI provider returned no text")
	}
	if msg.StopReason == anthropic.BetaStopReasonMaxTokens {
		text += "\n\n_(The generated report was cut off at the length limit.)_"
	}
	return Response{Text: text, Model: msg.Model}, nil
}
