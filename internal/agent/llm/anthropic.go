package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultAnthropicModel is used when ANTHROPIC_MODEL is unset.
const DefaultAnthropicModel = "claude-sonnet-5-5"

// Anthropic implements Client against the Anthropic Messages API using
// native tool_use / tool_result blocks and base64 image blocks.
type Anthropic struct {
	key     string
	model   string
	baseURL string
	hc      *http.Client
}

// NewAnthropic returns an Anthropic client. An empty model selects
// DefaultAnthropicModel.
func NewAnthropic(apiKey, model string) *Anthropic {
	if strings.TrimSpace(model) == "" {
		model = DefaultAnthropicModel
	}
	return &Anthropic{
		key:     apiKey,
		model:   model,
		baseURL: "https://api.anthropic.com",
		hc:      &http.Client{Timeout: 120 * time.Second},
	}
}

// SupportsVision reports true: Claude models accept image blocks.
func (a *Anthropic) SupportsVision() bool { return true }

// Chat implements Client.
func (a *Anthropic) Chat(ctx context.Context, system string, messages []Message, tools []Tool) (Reply, error) {
	body := map[string]any{
		"model":      a.model,
		"max_tokens": 1024,
		"system":     system,
		"messages":   anthropicMessages(messages),
	}
	if len(tools) > 0 {
		ts := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			ts = append(ts, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.Schema,
			})
		}
		body["tools"] = ts
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("x-api-key", a.key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return Reply{}, fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Reply{}, fmt.Errorf("anthropic read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Reply{}, fmt.Errorf("anthropic %s (model %q): %s", resp.Status, a.model, truncate(string(raw), 500))
	}
	var out struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Reply{}, fmt.Errorf("anthropic decode: %w", err)
	}
	rep := Reply{StopReason: out.StopReason}
	var texts []string
	for _, c := range out.Content {
		switch c.Type {
		case "text":
			if strings.TrimSpace(c.Text) != "" {
				texts = append(texts, c.Text)
			}
		case "tool_use":
			rep.ToolCalls = append(rep.ToolCalls, ToolCall{ID: c.ID, Name: c.Name, Args: c.Input})
		}
	}
	rep.Text = strings.Join(texts, "\n")
	return rep, nil
}

func anthropicMessages(msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		var blocks []map[string]any
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				blocks = append(blocks, map[string]any{"type": "text", "text": b.Text})
			case BlockImage:
				if len(b.Image) == 0 {
					continue
				}
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": "image/png",
						"data":       base64.StdEncoding.EncodeToString(b.Image),
					},
				})
			case BlockToolUse:
				in := b.Input
				if in == nil {
					in = map[string]any{}
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": in})
			case BlockToolResult:
				tr := map[string]any{"type": "tool_result", "tool_use_id": b.ID, "content": b.Content}
				if b.IsError {
					tr["is_error"] = true
				}
				blocks = append(blocks, tr)
			}
		}
		if len(blocks) == 0 {
			continue
		}
		out = append(out, map[string]any{"role": m.Role, "content": blocks})
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
