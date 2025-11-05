package decision

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// LoadStrategyPrompt 加载策略提示词
// strategyName: 策略名称（对应strategies文件夹下的文件名，不含.txt扩展名）
func LoadStrategyPrompt(strategyName string) (string, error) {
	// 获取策略文件路径（相对于当前工作目录或可执行文件目录）
	// 尝试多个可能的路径
	var baseDir string
	possiblePaths := []string{
		"strategies",                    // 当前工作目录
		"backend/strategies",            // 从项目根目录运行
		filepath.Join("..", "strategies"), // 从backend目录运行
	}
	
	for _, path := range possiblePaths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			baseDir = path
			break
		}
	}
	
	if baseDir == "" {
		return "", fmt.Errorf("找不到strategies文件夹，尝试过的路径: %v", possiblePaths)
	}
	
	log.Printf("📂 找到strategies文件夹: %s", baseDir)
	
	// 构建策略文件路径（策略名称即文件名，不含.txt扩展名）
	strategyFileName := strategyName
	if !strings.HasSuffix(strategyFileName, ".txt") {
		strategyFileName = strategyFileName + ".txt"
	}
	strategyPath := filepath.Join(baseDir, strategyFileName)
	
	// 加载策略提示词文件
	strategyPrompt, err := os.ReadFile(strategyPath)
	if err != nil {
		return "", fmt.Errorf("加载策略提示词失败 (%s): %w", strategyPath, err)
	}
	log.Printf("✅ 已加载策略提示词: %s (%d 字符)", strategyPath, len(strategyPrompt))
	
	finalPrompt := string(strategyPrompt)
	log.Printf("✅ 策略提示词加载完成: '%s' = %d 字符", strategyName, len(finalPrompt))
	
	return finalPrompt, nil
}

