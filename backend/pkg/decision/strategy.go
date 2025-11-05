package decision

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// LoadStrategyPrompt 加载策略提示词
// strategyName: 策略名称（对应strategies文件夹下的策略文件夹名）
// preference: 策略偏好（可选）
func LoadStrategyPrompt(strategyName, preference string) (string, error) {
	// 获取策略文件路径（相对于当前工作目录或可执行文件目录）
	// 尝试多个可能的路径
	var baseDir string
	possiblePaths := []string{
		"strategies",                    // 当前工作目录
		"backend/strategies",            // 从项目根目录运行
		filepath.Join("..", "strategies"), // 从backend目录运行
	}
	
	for _, path := range possiblePaths {
		if _, err := os.Stat(filepath.Join(path, "base_prompt.txt")); err == nil {
			baseDir = path
			break
		}
	}
	
	if baseDir == "" {
		return "", fmt.Errorf("找不到strategies文件夹，尝试过的路径: %v", possiblePaths)
	}
	
	log.Printf("📂 找到strategies文件夹: %s", baseDir)
	
	// 加载base提示词
	basePath := filepath.Join(baseDir, "base_prompt.txt")
	basePrompt, err := os.ReadFile(basePath)
	if err != nil {
		return "", fmt.Errorf("加载base提示词失败 (%s): %w", basePath, err)
	}
	log.Printf("✅ 已加载base提示词: %s (%d 字符)", basePath, len(basePrompt))
	
	// 加载策略特定提示词
	strategyPath := filepath.Join(baseDir, strategyName, "strategy_prompt.txt")
	strategyPrompt, err := os.ReadFile(strategyPath)
	if err != nil {
		return "", fmt.Errorf("加载策略提示词失败 (%s): %w", strategyPath, err)
	}
	log.Printf("✅ 已加载策略提示词: %s (%d 字符)", strategyPath, len(strategyPrompt))
	
	// 组合提示词
	var sb strings.Builder
	
	// 添加策略标识（让AI明确知道使用的策略）
	sb.WriteString(fmt.Sprintf("# 🎯 当前策略: %s\n\n", strategyName))
	
	sb.WriteString(string(basePrompt))
	sb.WriteString("\n\n")
	sb.WriteString(string(strategyPrompt))
	
	// 如果有偏好设置，从文件读取偏好说明
	if preference != "" {
		sb.WriteString("\n\n# 🎨 策略偏好\n\n")
		sb.WriteString(fmt.Sprintf("当前策略偏好: **%s**\n\n", preference))
		
		// 尝试从preferences文件夹读取偏好文件
		preferencePath := filepath.Join(baseDir, "preferences", strings.ToLower(preference)+".txt")
		preferenceContent, err := os.ReadFile(preferencePath)
		if err == nil {
			sb.WriteString(string(preferenceContent))
			sb.WriteString("\n")
			log.Printf("✅ 已加载偏好文件: %s", preferencePath)
		} else {
			// 如果文件不存在，只显示偏好名称
			log.Printf("⚠️  偏好文件不存在: %s，仅显示偏好名称", preferencePath)
			sb.WriteString(fmt.Sprintf("**偏好**: %s\n\n", preference))
		}
	}
	
	finalPrompt := sb.String()
	log.Printf("✅ 策略提示词组合完成: '%s' + '%s' = %d 字符", strategyName, preference, len(finalPrompt))
	
	return finalPrompt, nil
}

