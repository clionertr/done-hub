// Package plugins 提供 done-hub 请求处理插件框架。
// 插件通过注册钩子拦截并修改 /claude/v1/messages 请求，实现自定义转发逻辑。
package plugins

import (
	"done-hub/common/logger"
	"done-hub/providers/claude"
	"fmt"

	"github.com/gin-gonic/gin"
)

// ClaudeRequestContext 是传给请求钩子的上下文。
// 钩子可以修改 Request 的任意字段（模型、工具、消息、system），
// 修改结果会作用于后续的格式转换与上游转发。
type ClaudeRequestContext struct {
	Gin       *gin.Context
	Request   *claude.ClaudeRequest
	ModelName string // 渠道映射后的模型名
}

// ClaudeRequestHook 处理 /claude/v1/messages 请求的钩子。
type ClaudeRequestHook func(ctx *ClaudeRequestContext) error

var claudeHooks []ClaudeRequestHook

// RegisterClaudeHook 注册请求钩子，按注册顺序依次执行。
func RegisterClaudeHook(hook ClaudeRequestHook) {
	claudeHooks = append(claudeHooks, hook)
}

// ProcessClaudeRequest 依次执行所有已注册钩子。
// 单个钩子失败只记录日志，不阻断主流程，避免插件故障拖垮正常请求。
func ProcessClaudeRequest(ctx *ClaudeRequestContext) {
	for _, hook := range claudeHooks {
		if err := hook(ctx); err != nil {
			logger.LogError(ctx.Gin.Request.Context(), fmt.Sprintf("plugin hook error: %v", err))
		}
	}
}
