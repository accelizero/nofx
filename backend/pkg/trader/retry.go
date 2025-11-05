package trader

import (
	"fmt"
	"log"
	"time"
)

// RetryConfig 重试配置
type RetryConfig struct {
	MaxRetries    int           // 最大重试次数
	InitialDelay  time.Duration // 初始延迟
	MaxDelay      time.Duration // 最大延迟
	BackoffFactor float64       // 退避因子
}

// DefaultRetryConfig 默认重试配置
var DefaultRetryConfig = RetryConfig{
	MaxRetries:    3,
	InitialDelay:  1 * time.Second,
	MaxDelay:      10 * time.Second,
	BackoffFactor: 2.0,
}

// RetryableFunc 可重试的函数类型
type RetryableFunc func() error

// RetryWithBackoff 使用指数退避重试执行函数
func RetryWithBackoff(fn RetryableFunc, config RetryConfig) error {
	var lastErr error
	
	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			// 计算延迟时间（指数退避）
			delay := time.Duration(float64(config.InitialDelay) * float64(config.BackoffFactor) * float64(attempt-1))
			if delay > config.MaxDelay {
				delay = config.MaxDelay
			}
			log.Printf("  🔄 重试 %d/%d (延迟 %.1f秒)...", attempt, config.MaxRetries, delay.Seconds())
			time.Sleep(delay)
		}
		
		err := fn()
		if err == nil {
			if attempt > 0 {
				log.Printf("  ✓ 重试成功（第 %d 次尝试）", attempt+1)
			}
			return nil
		}
		
		lastErr = err
		log.Printf("  ❌ 尝试 %d/%d 失败: %v", attempt+1, config.MaxRetries+1, err)
	}
	
	return fmt.Errorf("重试 %d 次后仍然失败: %w", config.MaxRetries+1, lastErr)
}

