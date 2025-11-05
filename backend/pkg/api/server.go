package api

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"backend/pkg/manager"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// rateLimitEntry 限流条目（用于存储每个IP的请求计数）
type rateLimitEntry struct {
	count      int
	lastReset  time.Time
	lastAccess time.Time // 最后访问时间，用于清理
	mu         sync.Mutex
}

// rateLimitStore 限流存储（IP -> 限流条目）
var rateLimitStore = make(map[string]*rateLimitEntry)
var rateLimitMu sync.RWMutex

// rateLimitCleanupInterval 限流存储清理间隔（5分钟）
const rateLimitCleanupInterval = 5 * time.Minute

// rateLimitMaxIdleTime 限流条目最大空闲时间（30分钟未访问则删除）
const rateLimitMaxIdleTime = 30 * time.Minute

// init 启动定期清理goroutine
func init() {
	go rateLimitCleanup()
}

// rateLimitCleanup 定期清理过期的限流条目
func rateLimitCleanup() {
	ticker := time.NewTicker(rateLimitCleanupInterval)
	defer ticker.Stop()
	
	for range ticker.C {
		now := time.Now()
		rateLimitMu.Lock()
		for ip, entry := range rateLimitStore {
			entry.mu.Lock()
			lastAccess := entry.lastAccess
			entry.mu.Unlock()
			
			// 如果超过最大空闲时间，删除该条目
			if now.Sub(lastAccess) > rateLimitMaxIdleTime {
				delete(rateLimitStore, ip)
			}
		}
		rateLimitMu.Unlock()
	}
}

// rateLimitMiddleware API请求限流中间件（基于IP）
func rateLimitMiddleware(rps int) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 获取客户端IP
		clientIP := c.ClientIP()
		if clientIP == "" {
			clientIP = c.RemoteIP()
		}
		
		// 获取或创建限流条目
		rateLimitMu.RLock()
		entry, exists := rateLimitStore[clientIP]
		rateLimitMu.RUnlock()
		
		if !exists {
			rateLimitMu.Lock()
			entry = &rateLimitEntry{
				count:      0,
				lastReset:  time.Now(),
				lastAccess: time.Now(),
			}
			rateLimitStore[clientIP] = entry
			rateLimitMu.Unlock()
		}
		
		// 检查并更新计数
		entry.mu.Lock()
		defer entry.mu.Unlock()
		
		// 更新最后访问时间
		entry.lastAccess = time.Now()
		
		// 如果超过1秒，重置计数
		if time.Since(entry.lastReset) >= time.Second {
			entry.count = 0
			entry.lastReset = time.Now()
		}
		
		// 检查是否超过限制
		if entry.count >= rps {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "请求过于频繁，请稍后再试",
			})
			c.Abort()
			return
		}
		
		// 增加计数
		entry.count++
		
		c.Next()
	}
}

// Server HTTP API服务器
type Server struct {
	router        *gin.Engine
	traderManager *manager.TraderManager
	port          int
	httpServer    *http.Server
	allowedOrigins []string  // 允许的CORS来源
	enableRateLimit bool    // 是否启用限流
	rateLimitRPS    int     // 限流速率（请求/秒）
}

// NewServer 创建API服务器
func NewServer(traderManager *manager.TraderManager, port int, allowedOrigins []string, enableRateLimit bool, rateLimitRPS int) *Server {
	// 设置为Release模式（减少日志输出）
	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()

	// 启用CORS（使用配置的允许来源）
	router.Use(corsMiddleware(allowedOrigins))

	// 启用限流（如果配置启用）
	if enableRateLimit {
		router.Use(rateLimitMiddleware(rateLimitRPS))
	}

	s := &Server{
		router:        router,
		traderManager: traderManager,
		port:          port,
		allowedOrigins: allowedOrigins,
		enableRateLimit: enableRateLimit,
		rateLimitRPS:    rateLimitRPS,
	}

	// 设置路由
	s.setupRoutes()

	return s
}

// corsMiddleware CORS中间件（支持配置允许的来源）
func corsMiddleware(allowedOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		
		// 如果配置了允许的来源列表，检查是否在允许列表中
		if len(allowedOrigins) > 0 {
			allowed := false
			for _, allowedOrigin := range allowedOrigins {
				if origin == allowedOrigin {
					allowed = true
					break
				}
			}
			if allowed {
				c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			}
			// 如果不在允许列表中，不设置CORS头，浏览器会拒绝请求
		} else {
			// 如果allowedOrigins为空数组，允许所有来源（仅用于开发环境）
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		}
		
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusOK)
			return
		}

		c.Next()
	}
}

// setupRoutes 设置路由
func (s *Server) setupRoutes() {
	// 健康检查
	s.router.Any("/health", s.handleHealth)

	// API路由组
	api := s.router.Group("/api")
	{
		// 竞赛总览
		api.GET("/competition", s.handleCompetition)

		// Trader列表
		api.GET("/traders", s.handleTraderList)

		// 指定trader的数据（使用query参数 ?trader_id=xxx）
		api.GET("/status", s.handleStatus)
		api.GET("/account", s.handleAccount)
		api.GET("/positions", s.handlePositions)
		api.GET("/decisions", s.handleDecisions)
		api.GET("/decisions/latest", s.handleLatestDecisions)
		api.GET("/statistics", s.handleStatistics)
		api.GET("/equity-history", s.handleEquityHistory)
		api.GET("/performance", s.handlePerformance)
	}
}

// handleHealth 健康检查
func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// getTraderFromQuery 从query参数获取trader_id
func (s *Server) getTraderFromQuery(c *gin.Context) (string, error) {
	traderID := c.Query("trader_id")
	if traderID == "" {
		// 如果没有指定trader_id，返回第一个trader
		ids := s.traderManager.GetTraderIDs()
		if len(ids) == 0 {
			return "", fmt.Errorf("没有可用的trader")
		}
		traderID = ids[0]
	}
	return traderID, nil
}

// handleCompetition 竞赛总览（对比所有trader）
func (s *Server) handleCompetition(c *gin.Context) {
	comparison, err := s.traderManager.GetComparisonData()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("获取对比数据失败: %v", err),
		})
		return
	}
	c.JSON(http.StatusOK, comparison)
}

// handleTraderList trader列表
func (s *Server) handleTraderList(c *gin.Context) {
	traders := s.traderManager.GetAllTraders()
	result := make([]map[string]interface{}, 0, len(traders))

	for _, t := range traders {
		result = append(result, map[string]interface{}{
			"trader_id":   t.GetID(),
			"trader_name": t.GetName(),
			"ai_model":    t.GetAIModel(),
		})
	}

	c.JSON(http.StatusOK, result)
}

// handleStatus 系统状态
func (s *Server) handleStatus(c *gin.Context) {
	traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	status := trader.GetStatus()
	c.JSON(http.StatusOK, status)
}

// handleAccount 账户信息
func (s *Server) handleAccount(c *gin.Context) {
	traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	log.Printf("📊 收到账户信息请求 [%s]", trader.GetName())
	account, err := trader.GetAccountInfo()
	if err != nil {
		log.Printf("❌ 获取账户信息失败 [%s]: %v", trader.GetName(), err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("获取账户信息失败: %v", err),
		})
		return
	}

	log.Printf("✓ 返回账户信息 [%s]: 净值=%.2f, 可用=%.2f, 盈亏=%.2f (%.2f%%)",
		trader.GetName(),
		account["total_equity"],
		account["available_balance"],
		account["total_pnl"],
		account["total_pnl_pct"])
	c.JSON(http.StatusOK, account)
}

// handlePositions 持仓列表
func (s *Server) handlePositions(c *gin.Context) {
	traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	positions, err := trader.GetPositions()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("获取持仓列表失败: %v", err),
		})
		return
	}

	c.JSON(http.StatusOK, positions)
}

// handleDecisions 决策日志列表
func (s *Server) handleDecisions(c *gin.Context) {
	traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// 获取所有历史决策记录（从数据库）
	records, err := trader.GetDecisionRecordsFromDB(10000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("获取决策日志失败: %v", err),
		})
		return
	}
	c.JSON(http.StatusOK, records)
}

// handleLatestDecisions 最新决策日志（最近5条，最新的在前）
func (s *Server) handleLatestDecisions(c *gin.Context) {
	traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	records, err := trader.GetDecisionRecordsFromDB(5)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("获取决策日志失败: %v", err),
		})
		return
	}

	// 数据库查询已按时间逆序排列，最新的在前，无需反转
	c.JSON(http.StatusOK, records)
}

// handleStatistics 统计信息
func (s *Server) handleStatistics(c *gin.Context) {
	traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	stats, err := trader.GetStatisticsFromDB()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("获取统计信息失败: %v", err),
		})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// handleEquityHistory 收益率历史数据
func (s *Server) handleEquityHistory(c *gin.Context) {
	traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// 获取尽可能多的历史数据（几天的数据）
	// 每3分钟一个周期：10000条 = 约20天的数据
	records, err := trader.GetDecisionRecordsFromDB(10000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("获取历史数据失败: %v", err),
		})
		return
	}

	// 构建收益率历史数据点
	type EquityPoint struct {
		Timestamp        string  `json:"timestamp"`
		TotalEquity      float64 `json:"total_equity"`      // 账户净值（wallet + unrealized）
		AvailableBalance float64 `json:"available_balance"` // 可用余额
		TotalPnL         float64 `json:"total_pnl"`         // 总盈亏（相对初始余额）
		TotalPnLPct      float64 `json:"total_pnl_pct"`     // 总盈亏百分比
		InitialBalance   float64 `json:"initial_balance"`   // 初始余额（用于前端计算一致性）
		PositionCount    int     `json:"position_count"`    // 持仓数量
		MarginUsedPct    float64 `json:"margin_used_pct"`   // 保证金使用率
		CycleNumber      int     `json:"cycle_number"`
	}

	// 从AutoTrader获取初始余额（用于计算盈亏百分比）
	// 优先使用配置的initialBalance，确保与GetAccountInfo返回的值一致
	initialBalance := 0.0
	
	// 方法1：从GetStatus获取（最可靠）
	if status := trader.GetStatus(); status != nil {
		if ib, ok := status["initial_balance"].(float64); ok && ib > 0 {
			initialBalance = ib
		}
	}
	
	// 方法2：如果无法从status获取，尝试从trader实例直接获取（需要类型断言）
	if initialBalance == 0 {
		// 注意：这里需要根据实际的trader接口进行调整
		// 如果trader是AutoTrader类型，可以直接访问initialBalance字段
		// 但为了保持接口一致性，优先使用GetStatus()
	}
	
	// 方法3：如果无法获取，且有历史记录，则从第一条记录获取（不推荐，但作为fallback）
	if initialBalance == 0 && len(records) > 0 {
		// 第一条记录的equity作为初始余额（可能不准确，因为可能已有持仓）
		initialBalance = records[0].AccountState.TotalBalance
		log.Printf("⚠️  使用第一条记录的equity作为初始余额: %.2f（建议检查配置）", initialBalance)
	}

	// 如果还是无法获取，返回错误
	if initialBalance == 0 {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "无法获取初始余额",
		})
		return
	}

	var history []EquityPoint
	for _, record := range records {
		// TotalBalance字段实际存储的是TotalEquity
		totalEquity := record.AccountState.TotalBalance
		// TotalUnrealizedProfit字段实际存储的是TotalPnL（相对初始余额）
		totalPnL := record.AccountState.TotalUnrealizedProfit

		// 如果数据库中存储的P&L为0，或者看起来不正确的（比如P&L等于初始余额），则使用equity - initialBalance重新计算
		// This handles cases where the stored P&L value might be incorrect
		if totalPnL == 0 || math.Abs(totalPnL-initialBalance) < 0.01 { // Allow small floating point differences
			totalPnL = totalEquity - initialBalance
		}

		// 计算盈亏百分比
		totalPnLPct := 0.0
		if initialBalance > 0 {
			totalPnLPct = (totalPnL / initialBalance) * 100
		}

		history = append(history, EquityPoint{
			Timestamp:        record.Timestamp.Format("2006-01-02 15:04:05"),
			TotalEquity:      totalEquity,
			AvailableBalance: record.AccountState.AvailableBalance,
			TotalPnL:         totalPnL,
			TotalPnLPct:      totalPnLPct,
			InitialBalance:   initialBalance, // 添加初始余额字段，确保前端可以使用
			PositionCount:    record.AccountState.PositionCount,
			MarginUsedPct:    record.AccountState.MarginUsedPct,
			CycleNumber:      record.CycleNumber,
		})
	}

	// 确保数据按时间顺序排列（从旧到新，从左到右）- 如果数据库中是反序的，需要反转
	if len(history) > 1 {
		// 检查第一个记录是否比最后一个记录更早，如果不是则反转数组
		firstTime, _ := time.Parse("2006-01-02 15:04:05", history[0].Timestamp)
		lastTime, _ := time.Parse("2006-01-02 15:04:05", history[len(history)-1].Timestamp)
		
		if firstTime.After(lastTime) {
			// 如果第一个时间比最后一个时间晚，说明是反序的，需要反转
			for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
				history[i], history[j] = history[j], history[i]
			}
		}
	}

	c.JSON(http.StatusOK, history)
}

// handlePerformance AI历史表现分析（用于展示AI学习和反思）
func (s *Server) handlePerformance(c *gin.Context) {
	traderID, err := s.getTraderFromQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	trader, err := s.traderManager.GetTrader(traderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// 分析所有历史交易表现（从数据库获取）
	// 使用一个很大的数字（10000）来确保获取所有记录
	performance, err := trader.GetPerformanceFromDB(10000)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("分析历史表现失败: %v", err),
		})
		return
	}
	c.JSON(http.StatusOK, performance)
}

// Start 启动服务器
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("🌐 API服务器启动在 http://localhost%s", addr)
	log.Printf("📊 API文档:")
	log.Printf("  • GET  /api/competition      - 竞赛总览（对比所有trader）")
	log.Printf("  • GET  /api/traders          - Trader列表")
	log.Printf("  • GET  /api/status?trader_id=xxx     - 指定trader的系统状态")
	log.Printf("  • GET  /api/account?trader_id=xxx    - 指定trader的账户信息")
	log.Printf("  • GET  /api/positions?trader_id=xxx  - 指定trader的持仓列表")
	log.Printf("  • GET  /api/decisions?trader_id=xxx  - 指定trader的决策日志")
	log.Printf("  • GET  /api/decisions/latest?trader_id=xxx - 指定trader的最新决策")
	log.Printf("  • GET  /api/statistics?trader_id=xxx - 指定trader的统计信息")
	log.Printf("  • GET  /api/equity-history?trader_id=xxx - 指定trader的收益率历史数据")
	log.Printf("  • GET  /api/performance?trader_id=xxx - 指定trader的AI学习表现分析")
	log.Printf("  • GET  /health               - 健康检查")
	log.Println()
	
	// 创建http.Server以便支持优雅关闭
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.router,
	}
	
	return s.httpServer.ListenAndServe()
}

// Shutdown 优雅关闭服务器
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	log.Printf("🛑 正在关闭API服务器...")
	return s.httpServer.Shutdown(ctx)
}
