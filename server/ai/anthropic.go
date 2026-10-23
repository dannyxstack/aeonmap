package ai

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// callAnthropic 通过 Claude 官方 SDK 调用，用结构化输出保证返回的 JSON 符合 schema。
func callAnthropic(ctx context.Context, o Options, prompt string, img []byte, schema map[string]any) (string, Usage, error) {
	opts := []option.RequestOption{option.WithAPIKey(o.APIKey), option.WithMaxRetries(2)}
	if o.BaseURL != "" && o.BaseURL != o.Provider.BaseURL {
		opts = append(opts, option.WithBaseURL(o.BaseURL))
	}
	client := anthropic.NewClient(opts...)
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(o.Model),
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: systemPrompt}},
		Messages: []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(
			anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
				Data: base64.StdEncoding.EncodeToString(img), MediaType: anthropic.BetaBase64ImageSourceMediaTypeImageJPEG}),
			anthropic.NewBetaTextBlock(prompt),
		)},
		OutputConfig: anthropic.BetaOutputConfigParam{Format: anthropic.BetaJSONOutputFormatParam{Schema: schema}},
	}
	// Opus 5 / Fable 5.1 的安全分类器偶尔会误拒；开启服务端回退，由其他模型接手
	if strings.HasPrefix(o.Model, "claude-opus-5") || strings.HasPrefix(o.Model, "claude-fable-5") {
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}
	resp, err := client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 401, 403:
				return "", Usage{}, fmt.Errorf("Claude 拒绝了 API Key(HTTP %d)", apiErr.StatusCode)
			case 404:
				return "", Usage{}, fmt.Errorf("Claude 找不到模型 %s(HTTP 404)", o.Model)
			case 429:
				return "", Usage{}, fmt.Errorf("Claude 限流或余额不足(HTTP 429)")
			}
		}
		return "", Usage{}, fmt.Errorf("调用 Claude 失败: %w", err)
	}
	u := Usage{Calls: 1, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens}
	switch resp.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return "", u, errors.New("Claude 拒绝处理这张图片")
	case anthropic.BetaStopReasonMaxTokens:
		return "", u, errors.New("Claude 输出超长被截断")
	}
	var sb strings.Builder
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String(), u, nil
}
