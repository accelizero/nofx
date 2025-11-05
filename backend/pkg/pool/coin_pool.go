package pool

import (
	"fmt"
	"log"
	"sort"
)

// defaultMainstreamCoins 默认主流币种池（从配置文件读取）
var defaultMainstreamCoins = []string{
	"BTCUSDT",
	"ETHUSDT",
	"SOLUSDT",
	"BNBUSDT",
	"XRPUSDT",
	"DOGEUSDT",
	"ADAUSDT",
	"HYPEUSDT",
}

// CoinPoolConfig 币种池配置
type CoinPoolConfig struct {
	UseDefaultCoins bool // 是否使用默认主流币种
}

var coinPoolConfig = CoinPoolConfig{
	UseDefaultCoins: false, // 默认不使用
}

// CoinInfo 币种信息
type CoinInfo struct {
	Pair            string  `json:"pair"`             // 交易对符号（例如：BTCUSDT）
	Score           float64 `json:"score"`            // 当前评分
	StartTime       int64   `json:"start_time"`       // 开始时间（Unix时间戳）
	StartPrice      float64 `json:"start_price"`      // 开始价格
	LastScore       float64 `json:"last_score"`       // 最新评分
	MaxScore        float64 `json:"max_score"`        // 最高评分
	MaxPrice        float64 `json:"max_price"`        // 最高价格
	IncreasePercent float64 `json:"increase_percent"` // 涨幅百分比
	IsAvailable     bool    `json:"-"`                // 是否可交易（内部使用）
}


// SetUseDefaultCoins 设置是否使用默认主流币种
func SetUseDefaultCoins(useDefault bool) {
	coinPoolConfig.UseDefaultCoins = useDefault
}

// SetDefaultCoins 设置默认主流币种列表
func SetDefaultCoins(coins []string) {
	if len(coins) > 0 {
		defaultMainstreamCoins = coins
		log.Printf("✓ 已设置默认币种池（共%d个币种）: %v", len(coins), coins)
	}
}

// GetCoinPool 获取币种池列表
func GetCoinPool() ([]CoinInfo, error) {
	// 使用默认币种列表
	if coinPoolConfig.UseDefaultCoins {
		log.Printf("✓ 已启用默认主流币种列表")
		return convertSymbolsToCoins(defaultMainstreamCoins), nil
	}

	// 如果未启用默认币种，也使用默认主流币种列表
	log.Printf("⚠️  未启用默认币种列表，使用默认主流币种列表")
	return convertSymbolsToCoins(defaultMainstreamCoins), nil
}


// GetAvailableCoins 获取可用的币种列表（过滤不可用的）
func GetAvailableCoins() ([]string, error) {
	coins, err := GetCoinPool()
	if err != nil {
		return nil, err
	}

	var symbols []string
	for _, coin := range coins {
		if coin.IsAvailable {
			// 确保symbol格式正确（转为大写USDT交易对）
			symbol := normalizeSymbol(coin.Pair)
			symbols = append(symbols, symbol)
		}
	}

	if len(symbols) == 0 {
		return nil, fmt.Errorf("没有可用的币种")
	}

	return symbols, nil
}

// GetTopRatedCoins 获取评分最高的N个币种（按评分从大到小排序）
func GetTopRatedCoins(limit int) ([]string, error) {
	coins, err := GetCoinPool()
	if err != nil {
		return nil, err
	}

	// 过滤可用的币种
	var availableCoins []CoinInfo
	for _, coin := range coins {
		if coin.IsAvailable {
			availableCoins = append(availableCoins, coin)
		}
	}

	if len(availableCoins) == 0 {
		return nil, fmt.Errorf("没有可用的币种")
	}

	// 按Score降序排序（使用标准库sort.Slice，性能更好）
	sort.Slice(availableCoins, func(i, j int) bool {
		return availableCoins[i].Score > availableCoins[j].Score
	})

	// 取前N个
	maxCount := limit
	if len(availableCoins) < maxCount {
		maxCount = len(availableCoins)
	}

	var symbols []string
	for i := 0; i < maxCount; i++ {
		symbol := normalizeSymbol(availableCoins[i].Pair)
		symbols = append(symbols, symbol)
	}

	return symbols, nil
}

// normalizeSymbol 标准化币种符号
func normalizeSymbol(symbol string) string {
	// 移除空格
	symbol = trimSpaces(symbol)

	// 转为大写
	symbol = toUpper(symbol)

	// 确保以USDT结尾
	if !endsWith(symbol, "USDT") {
		symbol = symbol + "USDT"
	}

	return symbol
}

// 辅助函数
func trimSpaces(s string) string {
	result := ""
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' {
			result += string(s[i])
		}
	}
	return result
}

func toUpper(s string) string {
	result := ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c = c - 'a' + 'A'
		}
		result += string(c)
	}
	return result
}

func endsWith(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

// convertSymbolsToCoins 将币种符号列表转换为CoinInfo列表
func convertSymbolsToCoins(symbols []string) []CoinInfo {
	coins := make([]CoinInfo, 0, len(symbols))
	for _, symbol := range symbols {
		coins = append(coins, CoinInfo{
			Pair:        symbol,
			Score:       0,
			IsAvailable: true,
		})
	}
	return coins
}

// MergedCoinPool 币种池
type MergedCoinPool struct {
	Coins          []CoinInfo          // 币种信息
	AllSymbols     []string            // 所有币种符号
	SymbolSources  map[string][]string // 每个币种的来源
}

// GetMergedCoinPool 获取币种池
func GetMergedCoinPool(limit int) (*MergedCoinPool, error) {
	// 获取评分最高的币种
	topSymbols, err := GetTopRatedCoins(limit)
	if err != nil {
		log.Printf("⚠️  获取币种池失败: %v", err)
		topSymbols = []string{} // 失败时用空列表
	}

	// 构建来源映射
	symbolSources := make(map[string][]string)
	for _, symbol := range topSymbols {
		symbolSources[symbol] = []string{"default"}
	}

	// 获取完整数据
	coins, _ := GetCoinPool()

	merged := &MergedCoinPool{
		Coins:         coins,
		AllSymbols:    topSymbols,
		SymbolSources: symbolSources,
	}

	return merged, nil
}
