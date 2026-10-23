package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// callOpenAI 调用 OpenAI 兼容的 /chat/completions 接口(通义千问、豆包、智谱、Kimi、混元、千帆等)。
// 各家对 response_format 的支持不一致，这里靠提示词约束输出 JSON，再宽松地提取。
func callOpenAI(ctx context.Context, o Options, prompt string, img []byte) (string, Usage, error) {
	body := map[string]any{
		"model":       o.Model,
		"temperature": 0.1,
		"max_tokens":  4096,
		"messages": []any{
			map[string]any{"role": "system", "content": systemPrompt},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "image_url", "image_url": map[string]any{
					"url": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(img)}},
				map[string]any{"type": "text", "text": prompt},
			}},
		},
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", Usage{}, fmt.Errorf("连接 %s 失败: %w", o.Provider.Label, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	json.Unmarshal(rb, &out)
	if resp.StatusCode != 200 || out.Error != nil {
		msg := truncate(strings.TrimSpace(string(rb)), 300)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		hint := ""
		switch resp.StatusCode {
		case 401, 403:
			hint = "(API Key 无效或无权限)"
		case 404:
			hint = "(接口地址或模型名不对)"
		case 429:
			hint = "(限流或余额不足)"
		}
		return "", Usage{}, fmt.Errorf("%s 返回 HTTP %d%s: %s", o.Provider.Label, resp.StatusCode, hint, msg)
	}
	u := Usage{Calls: 1, InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	if len(out.Choices) == 0 {
		return "", u, fmt.Errorf("%s 没有返回内容", o.Provider.Label)
	}
	if out.Choices[0].FinishReason == "length" {
		return "", u, fmt.Errorf("%s 输出超长被截断", o.Provider.Label)
	}
	return out.Choices[0].Message.Content, u, nil
}
