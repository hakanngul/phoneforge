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

// DefaultOllamaModel is used when OLLAMA_MODEL is unset.
const DefaultOllamaModel = "llama3.1"

// Ollama implements Client against a local Ollama server's /api/chat
// endpoint with tool calling.
type Ollama struct {
	host   string
	model  string
	vision bool
	hc     *http.Client
}

// NewOllama returns an Ollama client for host (e.g. "http://127.0.0.1:11434";
// the scheme is optional). An empty model selects DefaultOllamaModel. Set
// vision only when the model accepts images.
func NewOllama(host, model string, vision bool) *Ollama {
	if strings.TrimSpace(model) == "" {
		model = DefaultOllamaModel
	}
	return &Ollama{host: normalizeHost(host), model: model, vision: vision, hc: &http.Client{Timeout: 300 * time.Second}}
}

// SupportsVision reports whether the configured model is treated as vision
// capable (OLLAMA_VISION=1 or a well-known vision model name).
func (o *Ollama) SupportsVision() bool {
	if o.vision {
		return true
	}
	m := strings.ToLower(o.model)
	for _, k := range []string{"llava", "vision", "minicpm-v", "gemma3", "qwen2.5vl", "bakllava", "moondream"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

// Chat implements Client.
func (o *Ollama) Chat(ctx context.Context, system string, messages []Message, tools []Tool) (Reply, error) {
	body := map[string]any{
		"model":    o.model,
		"stream":   false,
		"messages": o.convert(system, messages),
	}
	if len(tools) > 0 {
		ts := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			ts = append(ts, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Schema,
				},
			})
		}
		body["tools"] = ts
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.host+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := o.hc.Do(req)
	if err != nil {
		return Reply{}, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Reply{}, fmt.Errorf("ollama read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Reply{}, fmt.Errorf("ollama %s (model %q): %s", resp.Status, o.model, truncate(string(raw), 500))
	}
	var out struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		DoneReason string `json:"done_reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Reply{}, fmt.Errorf("ollama decode: %w", err)
	}
	rep := Reply{Text: strings.TrimSpace(out.Message.Content), StopReason: out.DoneReason}
	for i, tc := range out.Message.ToolCalls {
		rep.ToolCalls = append(rep.ToolCalls, ToolCall{
			ID:   fmt.Sprintf("call_%d", i),
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}
	return rep, nil
}

func (o *Ollama) convert(system string, msgs []Message) []map[string]any {
	out := []map[string]any{{"role": "system", "content": system}}
	for _, m := range msgs {
		var text []string
		var images []string
		var toolCalls []map[string]any
		var toolMsgs []map[string]any
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				if strings.TrimSpace(b.Text) != "" {
					text = append(text, b.Text)
				}
			case BlockImage:
				if o.SupportsVision() && len(b.Image) > 0 {
					images = append(images, base64.StdEncoding.EncodeToString(b.Image))
				}
			case BlockToolUse:
				args := b.Input
				if args == nil {
					args = map[string]any{}
				}
				toolCalls = append(toolCalls, map[string]any{"function": map[string]any{"name": b.Name, "arguments": args}})
			case BlockToolResult:
				toolMsgs = append(toolMsgs, map[string]any{"role": "tool", "tool_name": b.Name, "content": b.Content})
			}
		}
		out = append(out, toolMsgs...)
		if len(text) == 0 && len(images) == 0 && len(toolCalls) == 0 {
			continue
		}
		msg := map[string]any{"role": m.Role, "content": strings.Join(text, "\n")}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		if len(images) > 0 {
			msg["images"] = images
		}
		out = append(out, msg)
	}
	return out
}
