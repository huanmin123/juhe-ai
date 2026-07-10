package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/platform/postgres"
	redishealth "juhe-ai/backend-go/internal/platform/redis"
)

type W0SmokeResult struct {
	Success      bool                            `json:"success"`
	Dependencies map[string]postgres.CheckResult `json:"dependencies"`
}

func RunW0Smoke(ctx context.Context, cfg config.Config, out io.Writer) error {
	missing := missingW0URLs(cfg)
	if len(missing) > 0 {
		return fmt.Errorf("W0 smoke 缺少必要配置: %v", missing)
	}

	deps := map[string]postgres.CheckResult{
		"postgres":   postgres.Check(ctx, cfg.PostgresURL),
		"redisCache": redishealth.Check(ctx, cfg.RedisCacheURL),
		"redisState": redishealth.Check(ctx, cfg.RedisStateURL),
		"asynqQueue": queue.Check(ctx, cfg.RedisQueueURL),
	}
	if err := queue.Smoke(ctx, cfg.RedisQueueURL); err != nil {
		deps["asynqQueue"] = postgres.CheckResult{Configured: true, Status: "error", Error: err.Error()}
	}

	result := W0SmokeResult{Success: true, Dependencies: redactDependencies(deps)}
	for _, dep := range result.Dependencies {
		if dep.Status != "ok" {
			result.Success = false
			break
		}
	}

	if err := json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("W0 smoke 未通过")
	}
	return nil
}

func redactDependencies(deps map[string]postgres.CheckResult) map[string]postgres.CheckResult {
	redacted := make(map[string]postgres.CheckResult, len(deps))
	for name, dep := range deps {
		if dep.Error != "" {
			dep.Error = "dependency check failed"
		}
		redacted[name] = dep
	}
	return redacted
}

func missingW0URLs(cfg config.Config) []string {
	var missing []string
	if cfg.PostgresURL == "" {
		missing = append(missing, "JUHE_AI_POSTGRES_URL")
	}
	if cfg.RedisCacheURL == "" {
		missing = append(missing, "JUHE_AI_REDIS_CACHE_URL")
	}
	if cfg.RedisStateURL == "" {
		missing = append(missing, "JUHE_AI_REDIS_STATE_URL")
	}
	if cfg.RedisQueueURL == "" {
		missing = append(missing, "JUHE_AI_REDIS_QUEUE_URL")
	}
	return missing
}
