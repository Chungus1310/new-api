package helper

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

// ApplyTokenReasoningDefault writes the key's default reasoning level into a
// request that carries none, in that request's own format. Any level the client
// sends wins, and a level in the model name (model-high) is applied later and
// overrides this one.
func ApplyTokenReasoningDefault(c *gin.Context, request dto.Request) {
	effort := common.GetContextKeyString(c, constant.ContextKeyTokenReasoningEffort)
	if effort == "" {
		return
	}
	switch req := request.(type) {
	case *dto.GeneralOpenAIRequest:
		if req.ReasoningEffort == "" && len(req.Reasoning) == 0 {
			req.ReasoningEffort = effort
		}
	case *dto.OpenAIResponsesRequest:
		if req.Reasoning == nil {
			req.Reasoning = &dto.Reasoning{Effort: effort}
		} else if req.Reasoning.Effort == "" && len(req.Reasoning.Mode) == 0 {
			req.Reasoning.Effort = effort
		}
	case *dto.ClaudeRequest:
		if req.Thinking != nil || req.GetEfforts() != "" {
			return
		}
		config := map[string]any{}
		if len(req.OutputConfig) > 0 && common.Unmarshal(req.OutputConfig, &config) != nil {
			return
		}
		config["effort"] = effort
		if encoded, err := common.Marshal(config); err == nil {
			req.OutputConfig = encoded
		}
	case *dto.GeminiChatRequest:
		// Gemini's thinkingLevel stops at high.
		level := effort
		if level == "max" {
			level = "high"
		}
		thinking := req.GenerationConfig.ThinkingConfig
		if thinking == nil {
			req.GenerationConfig.ThinkingConfig = &dto.GeminiThinkingConfig{ThinkingLevel: level}
		} else if thinking.ThinkingLevel == "" && thinking.ThinkingBudget == nil {
			thinking.ThinkingLevel = level
		}
	}
}
