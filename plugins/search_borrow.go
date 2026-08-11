package plugins

import (
	"bytes"
	"context"
	"done-hub/common/logger"
	"done-hub/providers/claude"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	webSearchTool2025 = "web_search_20250305"
	webSearchTool     = "web_search"
)

// searchBorrowConfig 搜索借用插件配置，全部来自环境变量：
//   - SEARCH_BORROW_ENABLED=true 启用插件
//   - SEARCH_BORROW_BASE_URL  搜索源端点（Anthropic 兼容，如 https://api.deepseek.com/anthropic）
//   - SEARCH_BORROW_API_KEY   搜索源 key
//   - SEARCH_BORROW_MODEL     执行搜索的模型（建议 flash，如 deepseek-v4-flash）
//   - SEARCH_BORROW_MODELS    需要借用搜索的目标模型，逗号分隔，默认 deepseek-v4-pro,deepseek-v4-pro[1m]
type searchBorrowConfig struct {
	enabled bool
	baseURL string
	apiKey  string
	model   string
	models  []string
	timeout time.Duration
}

var sbConfig searchBorrowConfig

func init() {
	sbConfig = searchBorrowConfig{
		enabled: os.Getenv("SEARCH_BORROW_ENABLED") == "true",
		baseURL: strings.TrimSuffix(os.Getenv("SEARCH_BORROW_BASE_URL"), "/"),
		apiKey:  os.Getenv("SEARCH_BORROW_API_KEY"),
		model:   os.Getenv("SEARCH_BORROW_MODEL"),
		timeout: 30 * time.Second,
	}
	if models := os.Getenv("SEARCH_BORROW_MODELS"); models != "" {
		for _, m := range strings.Split(models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			sbConfig.models = append(sbConfig.models, m)
		}
		}
	} else {
		sbConfig.models = []string{"deepseek-v4-pro", "deepseek-v4-pro[1m]"}
	}
	if sbConfig.enabled {
		RegisterClaudeHook(searchBorrowHook)
	}
}

// searchBorrowHook 把目标模型请求中的服务端 web_search 工具替换为真实搜索：
// 1. 用搜索源（flash 模型）执行搜索请求，拿到结果文本
// 2. 从原请求移除 web_search 工具（上游可能不支持，会 400）
// 3. 把搜索结果注入 system 消息，让 pro 基于搜索结果回答
func searchBorrowHook(ctx *ClaudeRequestContext) error {
	req := ctx.Request
	if !containsWebSearchTool(req.Tools) || !shouldBorrowSearch(ctx.ModelName) {
		return nil
	}
	if sbConfig.baseURL == "" || sbConfig.apiKey == "" || sbConfig.model == "" {
		// 未配置搜索源：移除搜索工具避免上游 400，不注入结果
		removeWebSearchTools(req)
		return nil
	}

	query := extractUserQuery(req)
	if query == "" {
		removeWebSearchTools(req)
		return nil
	}

	result, err := runSearch(ctx.Gin.Request.Context(), query)
	if err != nil {
		logger.LogError(ctx.Gin.Request.Context(), fmt.Sprintf("search_borrow search failed: %v", err))
		removeWebSearchTools(req)
		return nil
	}

	removeWebSearchTools(req)
	appendSystemText(req, result)
	logger.LogInfo(ctx.Gin.Request.Context(), fmt.Sprintf("search_borrow injected result model=%s query_chars=%d result_chars=%d",
		ctx.ModelName, len(query), len(result)))
	return nil
}

// runSearch 向搜索源发送 Anthropic 格式搜索请求（web_search_20250305 服务端工具），
// 返回提取后的结果文本。
func runSearch(ctx context.Context, query string) (string, error) {
	body, err := json.Marshal(claude.ClaudeRequest{
		Model:     sbConfig.model,
		MaxTokens: 512,
		Tools:     []claude.Tools{{Type: webSearchTool2025}},
		Messages:  []claude.Message{{Role: "user", Content: query}},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sbConfig.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", sbConfig.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: sbConfig.timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("search source returned %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}

	var cr claude.ClaudeResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return "", err
	}
	return extractResultText(&cr), nil
}

// extractResultText 从搜索源响应中提取文本：text 块与 web_search_tool_result 块的 content。
func extractResultText(cr *claude.ClaudeResponse) string {
	var parts []string
	for _, block := range cr.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case "web_search_tool_result":
			if text := anyToString(block.Content); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// extractUserQuery 提取请求中最后一条用户消息的文本作为搜索 query。
func extractUserQuery(req *claude.ClaudeRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != "user" {
			continue
		}
		if text := anyToString(msg.Content); text != "" {
			return text
		}
	}
	return ""
}

// anyToString 把 string 或 [{type:text,text}] 形式的内容转成纯文本。
func anyToString(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok && t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// appendSystemText 把搜索结果注入请求的 system 提示。
func appendSystemText(req *claude.ClaudeRequest, text string) {
	switch sys := req.System.(type) {
	case nil:
		req.System = "以下是本次对话触发的联网搜索到的资料，请优先基于这些资料回答：\n" + text
	case string:
		req.System = sys + "\n\n以下是本次对话触发的联网搜索到的资料，请优先基于这些资料回答：\n" + text
	case []any:
		req.System = append(sys, map[string]any{
			"type": "text",
			"text": "以下是本次对话触发的联网搜索到的资料，请优先基于这些资料回答：\n" + text,
		})
	default:
		req.System = text
	}
}

func containsWebSearchTool(tools []claude.Tools) bool {
	for _, t := range tools {
		if t.Type == webSearchTool2025 || t.Type == webSearchTool {
			return true
		}
	}
	return false
}

func removeWebSearchTools(req *claude.ClaudeRequest) {
	kept := req.Tools[:0]
	for _, t := range req.Tools {
		if t.Type == webSearchTool2025 || t.Type == webSearchTool {
			continue
		}
		kept = append(kept, t)
	}
	req.Tools = kept
}

// shouldBorrowSearch 判断目标模型是否需要借用搜索（按配置列表前缀匹配）。
func shouldBorrowSearch(modelName string) bool {
	for _, m := range sbConfig.models {
		if strings.HasPrefix(modelName, m) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
