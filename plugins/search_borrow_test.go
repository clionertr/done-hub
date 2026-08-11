package plugins

import (
	"done-hub/common/logger"
	"done-hub/providers/claude"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMain(m *testing.M) {
	logger.SetupLogger()
	os.Exit(m.Run())
}

func TestSearchBorrowHook(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/claude/v1/messages", nil)
	var searchModel string
	var searchTools []claude.Tools
	var searchMessages []claude.Message

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var req claude.ClaudeRequest
		json.NewDecoder(r.Body).Decode(&req)
		searchModel = req.Model
		searchTools = req.Tools
		searchMessages = req.Messages
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(claude.ClaudeResponse{
			Type:  "message",
			Model: req.Model,
			Content: []claude.ResContent{
				{Type: "web_search_tool_result", Content: "上海今日天气晴，26 度"},
				{Type: "text", Text: "根据搜索，上海今天晴天。"},
			},
		})
	}))
	defer mock.Close()

	saved := sbConfig
	sbConfig = searchBorrowConfig{
		enabled: true,
		baseURL: mock.URL,
		apiKey:  "test-key",
		model:   "deepseek-v4-flash",
		models:  []string{"deepseek-v4-pro", "deepseek-v4-pro[1m]"},
	}
	defer func() { sbConfig = saved }()

	req := &claude.ClaudeRequest{
		Model: "deepseek-v4-pro",
		Tools: []claude.Tools{
			{Type: "web_search_20250305"},
			{Type: "custom", Name: "Bash", Description: "run bash", InputSchema: map[string]any{"type": "object"}},
		},
		Messages: []claude.Message{
			{Role: "user", Content: "今天上海天气怎么样？"},
		},
	}

	err := searchBorrowHook(&ClaudeRequestContext{Gin: ginCtx, Request: req, ModelName: "deepseek-v4-pro"})
	if err != nil {
		t.Fatalf("hook error: %v", err)
	}

	// 1. 搜索源收到 flash 模型 + web_search 工具 + 用户 query
	if searchModel != "deepseek-v4-flash" {
		t.Errorf("search model = %q, want deepseek-v4-flash", searchModel)
	}
	if len(searchTools) != 1 || searchTools[0].Type != "web_search_20250305" {
		t.Errorf("search tools = %+v, want web_search_20250305", searchTools)
	}
	if len(searchMessages) != 1 || searchMessages[0].Content != "今天上海天气怎么样？" {
		t.Errorf("search messages = %+v", searchMessages)
	}

	// 2. 原请求的 web_search 工具被移除，Bash 保留
	if len(req.Tools) != 1 || req.Tools[0].Name != "Bash" {
		t.Errorf("req tools after hook = %+v, want only Bash", req.Tools)
	}

	// 3. system 注入了搜索结果
	sys, ok := req.System.(string)
	if !ok || !strings.Contains(sys, "上海今日天气晴") {
		t.Errorf("system = %v, want injected search result", req.System)
	}
}

func TestSearchBorrowSkipsNonTargetModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/claude/v1/messages", nil)
	saved := sbConfig
	sbConfig = searchBorrowConfig{models: []string{"deepseek-v4-pro"}}
	defer func() { sbConfig = saved }()

	req := &claude.ClaudeRequest{
		Model: "deepseek-v4-flash",
		Tools: []claude.Tools{{Type: "web_search_20250305"}},
		Messages: []claude.Message{{Role: "user", Content: "hi"}},
	}
	err := searchBorrowHook(&ClaudeRequestContext{Gin: ginCtx, Request: req, ModelName: "deepseek-v4-flash"})
	if err != nil {
		t.Fatalf("hook error: %v", err)
	}
	if len(req.Tools) != 1 {
		t.Errorf("flash model should not be touched, tools = %+v", req.Tools)
	}
}
