package config

import (
	"fmt"
	"os"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// TraderConfig 单个trader的配置
type TraderConfig struct {
	ID      string `toml:"id"`
	Name    string `toml:"name"`
	Enabled bool   `toml:"enabled"` // 是否启用该trader
	AIModel string `toml:"ai_model"` // "qwen" or "deepseek"

	// 交易平台选择
	Exchange string `toml:"exchange"` // "aster"

	// Aster配置
	AsterUser       string `toml:"aster_user,omitempty"`        // Aster主钱包地址
	AsterSigner     string `toml:"aster_signer,omitempty"`      // Aster API钱包地址
	AsterPrivateKey string `toml:"aster_private_key,omitempty"` // Aster API钱包私钥

	// AI配置
	QwenKey     string `toml:"qwen_key,omitempty"`
	DeepSeekKey string `toml:"deepseek_key,omitempty"`

	// 自定义AI API配置（支持任何OpenAI格式的API）
	CustomAPIURL    string `toml:"custom_api_url,omitempty"`
	CustomAPIKey    string `toml:"custom_api_key,omitempty"`
	CustomModelName string `toml:"custom_model_name,omitempty"`

	InitialBalance      float64 `toml:"initial_balance"`
	ScanIntervalMinutes int     `toml:"scan_interval_minutes"`
}

// LeverageConfig 杠杆配置
type LeverageConfig struct {
	BTCETHLeverage  int `toml:"btc_eth_leverage"` // BTC和ETH的杠杆倍数（主账户建议5-50，子账户≤5）
	AltcoinLeverage int `toml:"altcoin_leverage"` // 山寨币的杠杆倍数（主账户建议5-20，子账户≤5）
}

// AnalysisModeConfig 分析模式配置
type AnalysisModeConfig struct {
	Mode string `toml:"mode"` // "standard" 或 "multi_timeframe"，默认"standard"
	
	// 多时间框架分析配置（仅在mode="multi_timeframe"时生效）
	MultiTimeframe *MultiTimeframeConfig `toml:"multi_timeframe,omitempty"`
}

// MultiTimeframeConfig 多时间框架分析配置
type MultiTimeframeConfig struct {
	// 时间框架权重（总和应为1.0）
	Weights struct {
		Daily    float64 `toml:"daily"`     // 日线权重（默认0.35）
		Hourly4  float64 `toml:"hourly4"`   // 4小时权重（默认0.25）
		Hourly1  float64 `toml:"hourly1"`   // 1小时权重（默认0.2）
		Minute15 float64 `toml:"minute15"`   // 15分钟权重（默认0.15）
		Minute3  float64 `toml:"minute3"`   // 3分钟权重（默认0.05）
	} `toml:"weights"`
	
	// 一致性评分阈值
	MinConsistencyScore float64 `toml:"min_consistency_score"` // 最低一致性评分（默认0.5）
	
	// 是否启用缓存
	EnableCache bool `toml:"enable_cache"` // 默认true
	
	// 缓存TTL（秒）
	CacheTTL MultiTimeframeCacheTTL `toml:"cache_ttl"`
	
	// 回调入场策略配置（"顺大逆小"策略）
	PullbackEntry PullbackEntryConfig `toml:"pullback_entry"`
}

// PullbackEntryConfig 回调入场策略配置
type PullbackEntryConfig struct {
	Enable     bool    `toml:"enable"`      // 是否启用回调入场策略（默认true）
	BonusScore float64 `toml:"bonus_score"` // 回调入场加分（默认0.15，范围0-0.3）
}

// MultiTimeframeCacheTTL 多时间框架缓存TTL配置
type MultiTimeframeCacheTTL struct {
	Daily    int `toml:"daily"`    // 日线数据TTL（默认3600秒=1小时）
	Hourly4  int `toml:"hourly4"`  // 4小时数据TTL（默认900秒=15分钟）
	Hourly1  int `toml:"hourly1"`  // 1小时数据TTL（默认300秒=5分钟）
	Minute15 int `toml:"minute15"` // 15分钟数据TTL（默认60秒=1分钟）
	Minute3  int `toml:"minute3"` // 3分钟数据TTL（默认30秒）
}

// Config 总配置
type Config struct {
	Traders            []TraderConfig      `toml:"traders"`
	UseDefaultCoins    bool                `toml:"use_default_coins"` // 是否使用默认主流币种列表
	DefaultCoins       []string            `toml:"default_coins"`     // 默认主流币种池
	APIServerPort      int                 `toml:"api_server_port"`
	MaxDailyLoss        float64             `toml:"max_daily_loss"`          // 最大日亏损百分比（账户级别风控）
	MaxDrawdown         float64             `toml:"max_drawdown"`            // 最大回撤百分比（账户级别风控）
	StopTradingMinutes  int                 `toml:"stop_trading_minutes"`    // 触发风控后暂停时长（分钟）
	PositionStopLossPct float64             `toml:"position_stop_loss_pct"` // 单仓位止损百分比（默认10%）
	PositionTakeProfitPct float64           `toml:"position_take_profit_pct"` // 单仓位止盈百分比（可选，>0时强制止盈，≤0时由AI自行判断）
	Leverage            LeverageConfig      `toml:"leverage"`                // 杠杆配置
	SkipLiquidityCheck bool                `toml:"skip_liquidity_check"`    // 是否跳过流动性检查（默认false，开启后可以交易流动性差的币种）
	AnalysisMode       AnalysisModeConfig  `toml:"analysis_mode"`           // 分析模式配置
	Strategy           StrategyConfig      `toml:"strategy"`                // 交易策略配置
	
	// API服务器配置
	APIServerConfig   APIServerConfig    `toml:"api_server_config"`       // API服务器配置
}

// StrategyConfig 交易策略配置
type StrategyConfig struct {
	Name       string `toml:"name"`        // 策略名称（对应strategies文件夹下的策略文件夹名）
	Preference string `toml:"preference"` // 策略偏好（可选，用于策略的个性化定制）
}

// APIServerConfig API服务器配置
type APIServerConfig struct {
	AllowedOrigins []string `toml:"allowed_origins"` // 允许的CORS来源（空数组表示允许所有来源，生产环境应配置具体域名）
	EnableRateLimit bool    `toml:"enable_rate_limit"` // 是否启用API请求限流（默认true）
	RateLimitRPS    int     `toml:"rate_limit_rps"`    // 每个IP每秒允许的请求数（默认100）
}

// LoadConfig 从TOML文件加载配置
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var config Config
	
	// 解析TOML格式配置文件
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析TOML配置文件失败: %w", err)
	}
	

	// 设置默认值：如果use_default_coins未设置，则默认使用默认币种列表
	if !config.UseDefaultCoins {
		config.UseDefaultCoins = true
	}

	// 设置默认币种池（仅在配置文件中没有指定default_coins时才使用默认值）
	// 注意：如果配置文件明确指定了default_coins（即使只有1个），就不应该覆盖
	// TOML解析时，如果字段存在，len(config.DefaultCoins) 不会为0
	// 但如果字段不存在或为空数组，len(config.DefaultCoins) 为0
	if len(config.DefaultCoins) == 0 {
		config.DefaultCoins = []string{
			"BTCUSDT",
			"ETHUSDT",
			"SOLUSDT",
			"BNBUSDT",
			"XRPUSDT",
			"DOGEUSDT",
			"ADAUSDT",
			"HYPEUSDT",
		}
	}

	// 设置策略默认配置
	if config.Strategy.Name == "" {
		config.Strategy.Name = "sharpe_ratio" // 默认使用夏普比率策略
	}
	if config.Strategy.Preference == "" {
		config.Strategy.Preference = "balanced" // 默认平衡偏好
	}
	
	// 设置API服务器默认配置
	if config.APIServerConfig.RateLimitRPS <= 0 {
		config.APIServerConfig.RateLimitRPS = 100 // 默认100请求/秒
	}
	if !config.APIServerConfig.EnableRateLimit {
		config.APIServerConfig.EnableRateLimit = true // 默认启用限流
	}
	// 如果allowed_origins为空，开发环境默认允许localhost，生产环境应配置
	if len(config.APIServerConfig.AllowedOrigins) == 0 {
		config.APIServerConfig.AllowedOrigins = []string{
			"http://localhost:5173",
			"http://localhost:3000",
			"http://127.0.0.1:5173",
			"http://127.0.0.1:3000",
		}
	}

	// 验证配置
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("配置验证失败: %w", err)
	}

	return &config, nil
}

// Validate 验证配置有效性
func (c *Config) Validate() error {
	if len(c.Traders) == 0 {
		return fmt.Errorf("至少需要配置一个trader")
	}

	traderIDs := make(map[string]bool)
	for i, trader := range c.Traders {
		if trader.ID == "" {
			return fmt.Errorf("trader[%d]: ID不能为空", i)
		}
		if traderIDs[trader.ID] {
			return fmt.Errorf("trader[%d]: ID '%s' 重复", i, trader.ID)
		}
		traderIDs[trader.ID] = true

		if trader.Name == "" {
			return fmt.Errorf("trader[%d]: Name不能为空", i)
		}
		if trader.AIModel != "qwen" && trader.AIModel != "deepseek" && trader.AIModel != "custom" {
			return fmt.Errorf("trader[%d]: ai_model必须是 'qwen', 'deepseek' 或 'custom'", i)
		}

		// 验证交易平台配置
		if trader.Exchange == "" {
			trader.Exchange = "aster" // 默认使用Aster
		}
		if trader.Exchange != "aster" {
			return fmt.Errorf("trader[%d]: exchange必须是 'aster'", i)
		}

		// 验证Aster配置
		if trader.AsterUser == "" || trader.AsterSigner == "" || trader.AsterPrivateKey == "" {
			return fmt.Errorf("trader[%d]: 使用Aster时必须配置aster_user, aster_signer和aster_private_key", i)
		}

		// 验证扫描间隔
		if trader.ScanIntervalMinutes <= 0 {
			return fmt.Errorf("trader[%d]: scan_interval_minutes必须大于0", i)
		}
		if trader.ScanIntervalMinutes < 1 {
			return fmt.Errorf("trader[%d]: scan_interval_minutes建议至少1分钟", i)
		}
		if trader.ScanIntervalMinutes > 60 {
			return fmt.Errorf("trader[%d]: scan_interval_minutes不应超过60分钟", i)
		}

		// 验证初始余额
		if trader.InitialBalance <= 0 {
			return fmt.Errorf("trader[%d]: initial_balance必须大于0", i)
		}

		if trader.AIModel == "qwen" && trader.QwenKey == "" {
			return fmt.Errorf("trader[%d]: 使用Qwen时必须配置qwen_key", i)
		}
		if trader.AIModel == "deepseek" && trader.DeepSeekKey == "" {
			return fmt.Errorf("trader[%d]: 使用DeepSeek时必须配置deepseek_key", i)
		}
		if trader.AIModel == "custom" {
			if trader.CustomAPIURL == "" {
				return fmt.Errorf("trader[%d]: 使用自定义API时必须配置custom_api_url", i)
			}
			if trader.CustomAPIKey == "" {
				return fmt.Errorf("trader[%d]: 使用自定义API时必须配置custom_api_key", i)
			}
			if trader.CustomModelName == "" {
				return fmt.Errorf("trader[%d]: 使用自定义API时必须配置custom_model_name", i)
			}
		}
	}

	// 设置API服务器端口默认值
	if c.APIServerPort <= 0 {
		c.APIServerPort = 8080 // 默认8080端口
	}

	// 验证杠杆配置
	if c.Leverage.BTCETHLeverage <= 0 {
		return fmt.Errorf("leverage.btc_eth_leverage必须大于0")
	}
	if c.Leverage.BTCETHLeverage > 125 {
		return fmt.Errorf("leverage.btc_eth_leverage不应超过125（交易所上限）")
	}
	if c.Leverage.AltcoinLeverage <= 0 {
		return fmt.Errorf("leverage.altcoin_leverage必须大于0")
	}
	if c.Leverage.AltcoinLeverage > 125 {
		return fmt.Errorf("leverage.altcoin_leverage不应超过125（交易所上限）")
	}

	// 验证风险控制参数
	if c.MaxDailyLoss < 0 || c.MaxDailyLoss > 100 {
		return fmt.Errorf("max_daily_loss必须在0-100之间（百分比）")
	}
	if c.MaxDrawdown < 0 || c.MaxDrawdown > 100 {
		return fmt.Errorf("max_drawdown必须在0-100之间（百分比）")
	}
	if c.PositionStopLossPct < 0 || c.PositionStopLossPct > 100 {
		return fmt.Errorf("position_stop_loss_pct必须在0-100之间（百分比）")
	}
	if c.StopTradingMinutes < 0 {
		return fmt.Errorf("stop_trading_minutes不能为负数")
	}

	// 验证API服务器配置
	if c.APIServerPort <= 0 || c.APIServerPort > 65535 {
		return fmt.Errorf("api_server_port必须在1-65535之间")
	}
	if c.APIServerConfig.RateLimitRPS < 0 {
		return fmt.Errorf("api_server_config.rate_limit_rps不能为负数")
	}
	if c.APIServerConfig.RateLimitRPS > 10000 {
		return fmt.Errorf("api_server_config.rate_limit_rps不应超过10000（防止配置错误）")
	}
	if c.Leverage.BTCETHLeverage > 5 {
		fmt.Printf("⚠️  警告: BTC/ETH杠杆设置为%dx，如果使用子账户可能会失败（子账户限制≤5x）\n", c.Leverage.BTCETHLeverage)
	}
	if c.Leverage.AltcoinLeverage <= 0 {
		c.Leverage.AltcoinLeverage = 5 // 默认5倍（安全值，适配子账户）
	}
	if c.Leverage.AltcoinLeverage > 5 {
		fmt.Printf("⚠️  警告: 山寨币杠杆设置为%dx，如果使用子账户可能会失败（子账户限制≤5x）\n", c.Leverage.AltcoinLeverage)
	}

	// 设置分析模式默认值
	if c.AnalysisMode.Mode == "" {
		c.AnalysisMode.Mode = "standard" // 默认使用标准模式
	}
	if c.AnalysisMode.Mode != "standard" && c.AnalysisMode.Mode != "multi_timeframe" {
		return fmt.Errorf("analysis_mode.mode必须是 'standard' 或 'multi_timeframe'")
	}
	
	// 如果使用多时间框架模式，设置默认配置
	if c.AnalysisMode.Mode == "multi_timeframe" {
		if c.AnalysisMode.MultiTimeframe == nil {
			c.AnalysisMode.MultiTimeframe = &MultiTimeframeConfig{}
		}
		mt := c.AnalysisMode.MultiTimeframe
		
		// 设置默认权重
		if mt.Weights.Daily == 0 && mt.Weights.Hourly4 == 0 && mt.Weights.Hourly1 == 0 && mt.Weights.Minute15 == 0 && mt.Weights.Minute3 == 0 {
			mt.Weights.Daily = 0.35
			mt.Weights.Hourly4 = 0.25
			mt.Weights.Hourly1 = 0.2
			mt.Weights.Minute15 = 0.15
			mt.Weights.Minute3 = 0.05
		}
		
		// 验证权重总和
		weightSum := mt.Weights.Daily + mt.Weights.Hourly4 + mt.Weights.Hourly1 + mt.Weights.Minute15 + mt.Weights.Minute3
		if weightSum < 0.99 || weightSum > 1.01 {
			return fmt.Errorf("multi_timeframe.weights权重总和应为1.0，当前: %.2f", weightSum)
		}
		
		// 设置默认一致性阈值
		if mt.MinConsistencyScore == 0 {
			mt.MinConsistencyScore = 0.5
		}
		
		// 设置默认缓存配置
		if mt.CacheTTL.Daily == 0 {
			mt.CacheTTL.Daily = 3600    // 1小时
		}
		if mt.CacheTTL.Hourly4 == 0 {
			mt.CacheTTL.Hourly4 = 900   // 15分钟
		}
		if mt.CacheTTL.Hourly1 == 0 {
			mt.CacheTTL.Hourly1 = 300   // 5分钟
		}
		if mt.CacheTTL.Minute15 == 0 {
			mt.CacheTTL.Minute15 = 60   // 1分钟
		}
		if mt.CacheTTL.Minute3 == 0 {
			mt.CacheTTL.Minute3 = 30   // 30秒
		}
		
		// 设置默认缓存启用
		if !mt.EnableCache {
			mt.EnableCache = true // 默认启用缓存
		}
		
		// 设置默认回调入场策略配置
		// 注意：Enable字段的默认值处理：
		// - 如果用户在config.toml中显式设置了pullback_entry，则使用用户设置
		// - 如果用户未设置pullback_entry，则默认启用（在multiframe_analyzer.go中处理）
		if mt.PullbackEntry.BonusScore == 0 {
			// BonusScore为0表示未配置，保持0，让multiframe_analyzer.go使用默认值0.15
		} else {
			// 如果用户配置了BonusScore，验证范围
			if mt.PullbackEntry.BonusScore < 0 {
				mt.PullbackEntry.BonusScore = 0
			}
			if mt.PullbackEntry.BonusScore > 0.3 {
				mt.PullbackEntry.BonusScore = 0.3 // 最大加分0.3
			}
		}
	}

	return nil
}

// GetScanInterval 获取扫描间隔
func (tc *TraderConfig) GetScanInterval() time.Duration {
	return time.Duration(tc.ScanIntervalMinutes) * time.Minute
}
