package mcp

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Provider AI提供商类型
type Provider string

const (
	ProviderDeepSeek Provider = "deepseek"
	ProviderQwen     Provider = "qwen"
	ProviderCustom   Provider = "custom"
)

// Client AI API配置
type Client struct {
	Provider   Provider
	APIKey     string
	SecretKey  string // 阿里云需要
	BaseURL    string
	Model      string
	Timeout    time.Duration
	UseFullURL bool // 是否使用完整URL（不添加/chat/completions）
}

func New() *Client {
	// 默认配置
	var defaultClient = Client{
		Provider: ProviderDeepSeek,
		BaseURL:  "https://api.deepseek.com/v1",
		Model:    "deepseek-chat",
		Timeout:  300 * time.Second, // 增加到300秒（5分钟），因为AI需要分析大量数据和生成完整JSON响应
	}
	return &defaultClient
}

// SetDeepSeekAPIKey 设置DeepSeek API密钥
func (cfg *Client) SetDeepSeekAPIKey(apiKey string) {
	cfg.Provider = ProviderDeepSeek
	cfg.APIKey = apiKey
	cfg.BaseURL = "https://api.deepseek.com/v1"
	cfg.Model = "deepseek-chat"
}

// SetQwenAPIKey 设置阿里云Qwen API密钥
func (cfg *Client) SetQwenAPIKey(apiKey, secretKey string) {
	cfg.Provider = ProviderQwen
	cfg.APIKey = apiKey
	cfg.SecretKey = secretKey
	cfg.BaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	cfg.Model = "qwen-plus" // 可选: qwen-turbo, qwen-plus, qwen-max
}

// SetCustomAPI 设置自定义OpenAI兼容API
func (cfg *Client) SetCustomAPI(apiURL, apiKey, modelName string) {
	cfg.Provider = ProviderCustom
	cfg.APIKey = apiKey

	// 检查URL是否以#结尾，如果是则使用完整URL（不添加/chat/completions）
	if strings.HasSuffix(apiURL, "#") {
		cfg.BaseURL = strings.TrimSuffix(apiURL, "#")
		cfg.UseFullURL = true
	} else {
		cfg.BaseURL = apiURL
		cfg.UseFullURL = false
	}

	cfg.Model = modelName
	cfg.Timeout = 300 * time.Second // 增加到300秒（5分钟）
}

// SetClient 设置完整的AI配置（高级用户）
func (cfg *Client) SetClient(Client Client) {
	if Client.Timeout == 0 {
		Client.Timeout = 30 * time.Second
	}
	cfg = &Client
}

// CallWithMessages 使用 system + user prompt 调用AI API（推荐）
func (cfg *Client) CallWithMessages(systemPrompt, userPrompt string) (string, error) {
	if cfg.APIKey == "" {
		return "", fmt.Errorf("AI API密钥未设置，请先调用 SetDeepSeekAPIKey() 或 SetQwenAPIKey()")
	}

	// 重试配置
	maxRetries := 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			fmt.Printf("⚠️  AI API调用失败，正在重试 (%d/%d)...\n", attempt, maxRetries)
		}

		result, err := cfg.callOnce(systemPrompt, userPrompt)
		if err == nil {
			if attempt > 1 {
				fmt.Printf("✓ AI API重试成功\n")
			}
			return result, nil
		}

		lastErr = err
		// 如果不是网络错误，不重试
		if !isRetryableError(err) {
			return "", err
		}

		// 重试前等待
		if attempt < maxRetries {
			waitTime := time.Duration(attempt) * 2 * time.Second
			fmt.Printf("⏳ 等待%v后重试...\n", waitTime)
			time.Sleep(waitTime)
		}
	}

	return "", fmt.Errorf("重试%d次后仍然失败: %w", maxRetries, lastErr)
}

// callOnce 单次调用AI API（重构版：简化逻辑）
func (cfg *Client) callOnce(systemPrompt, userPrompt string) (string, error) {
	// 1. 构建请求
	req, err := cfg.buildRequest(systemPrompt, userPrompt)
	if err != nil {
		return "", err
	}

	// 2. 发送请求（使用带超时的context）
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	req = req.WithContext(ctx)
	client := &http.Client{Timeout: cfg.Timeout}

	startTime := time.Now()
	fmt.Printf("📡 正在调用AI API (超时设置: %v)...\n", cfg.Timeout)
	resp, err := client.Do(req)
	elapsed := time.Since(startTime)
	if err != nil {
		return "", cfg.handleRequestError(err, elapsed)
	}
	defer resp.Body.Close()

	fmt.Printf("✓ AI API响应头接收完成 (耗时: %v)\n", elapsed)

	// 3. 读取响应体（简化版）
	body, err := cfg.readResponseBody(ctx, resp, startTime)
	if err != nil {
		return "", err
	}

	// 4. 解析响应
	return cfg.parseResponse(body, resp.StatusCode)
}

// isRetryableError 判断错误是否可重试
func isRetryableError(err error) bool {
	errStr := err.Error()
	// 网络错误、超时、EOF、空响应等可以重试
	retryableErrors := []string{
		"EOF",
		"timeout",
		"deadline exceeded",
		"connection reset",
		"connection refused",
		"temporary failure",
		"no such host",
		"Client.Timeout exceeded",
		"响应体为空",  // 服务器端问题，可以重试
		"读取响应体",   // 读取相关错误，可能是临时问题
	}
	for _, retryable := range retryableErrors {
		if strings.Contains(errStr, retryable) {
			return true
		}
	}
	return false
}

// buildRequest 构建HTTP请求
func (cfg *Client) buildRequest(systemPrompt, userPrompt string) (*http.Request, error) {
	// 构建 messages 数组
	messages := []map[string]string{}

	// 如果有 system prompt，添加 system message
	if systemPrompt != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": systemPrompt,
		})
	}

	// 添加 user message
	messages = append(messages, map[string]string{
		"role":    "user",
		"content": userPrompt,
	})

	// 构建请求体
	requestBody := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"temperature": 0.5, // 降低temperature以提高JSON格式稳定性
		"max_tokens":  4000, // 增加到4000，因为提示词较长且需要完整JSON响应
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 创建HTTP请求
	var url string
	if cfg.UseFullURL {
		url = cfg.BaseURL
	} else {
		url = fmt.Sprintf("%s/chat/completions", cfg.BaseURL)
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "identity") // 不请求压缩，避免解压缩错误

	// 根据不同的Provider设置认证方式
	switch cfg.Provider {
	case ProviderDeepSeek:
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.APIKey))
	case ProviderQwen:
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.APIKey))
	default:
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", cfg.APIKey))
	}

	return req, nil
}

// getBodyReader 获取响应体的Reader（处理压缩）
func (cfg *Client) getBodyReader(resp *http.Response) (io.Reader, error) {
	contentEncoding := resp.Header.Get("Content-Encoding")
	
	if contentEncoding == "gzip" {
		fmt.Printf("  🔓 检测到gzip压缩，开始解压缩...\n")
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("创建gzip解压器失败: %w（可能响应体已损坏）", err)
		}
		return gzReader, nil
	} else if contentEncoding != "" && contentEncoding != "identity" {
		fmt.Printf("  ⚠️  未知的Content-Encoding: %s，尝试直接读取\n", contentEncoding)
	}
	
	return resp.Body, nil
}

// readResponseBody 读取响应体（简化版）
func (cfg *Client) readResponseBody(ctx context.Context, resp *http.Response, startTime time.Time) ([]byte, error) {
	contentLength := resp.Header.Get("Content-Length")
	contentEncoding := resp.Header.Get("Content-Encoding")
	
	if contentLength == "" {
		fmt.Printf("📥 开始读取响应体 (使用分块传输，无Content-Length头")
	} else {
		fmt.Printf("📥 开始读取响应体 (Content-Length: %s", contentLength)
	}
	if contentEncoding != "" {
		fmt.Printf(", Content-Encoding: %s", contentEncoding)
	}
	fmt.Printf(")...\n")
	
	// 处理压缩
	bodyReader, err := cfg.getBodyReader(resp)
	if err != nil {
		return nil, err
	}
	
	// 如果是gzip reader，需要关闭
	var needClose bool
	var closer io.Closer
	if gzReader, ok := bodyReader.(*gzip.Reader); ok {
		needClose = true
		closer = gzReader
	}
	
	if needClose {
		defer closer.Close()
	}
	
	// 限制最大大小（防止内存溢出）
	maxBodySize := 10 * 1024 * 1024 // 10MB
	limitedReader := io.LimitReader(bodyReader, int64(maxBodySize))
	
	// 使用context控制超时，在goroutine中读取
	bodyChan := make(chan []byte, 1)
	errChan := make(chan error, 1)
	
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errChan <- fmt.Errorf("读取响应体时发生panic: %v", r)
			}
		}()
		
		body, err := io.ReadAll(limitedReader)
		if err != nil {
			errChan <- fmt.Errorf("读取响应体失败: %w", err)
			return
		}
		
		if len(body) == 0 {
			errChan <- fmt.Errorf("响应体为空（服务器可能没有发送数据或连接过早关闭）")
			return
		}
		
		bodyChan <- body
	}()
	
	readStartTime := time.Now()
	select {
	case body := <-bodyChan:
		readElapsed := time.Since(readStartTime)
		totalElapsed := time.Since(startTime)
		fmt.Printf("✓ 响应体读取完成 (读取耗时: %v, 总耗时: %v, 大小: %d 字节)\n", readElapsed, totalElapsed, len(body))
		return body, nil
	case err := <-errChan:
		readElapsed := time.Since(readStartTime)
		totalElapsed := time.Since(startTime)
		return nil, fmt.Errorf("读取响应失败 (读取耗时: %v，总耗时: %v): %w", readElapsed, totalElapsed, err)
	case <-ctx.Done():
		readElapsed := time.Since(readStartTime)
		totalElapsed := time.Since(startTime)
		return nil, fmt.Errorf("读取响应体超时 (读取耗时: %v，总耗时: %v，超时设置: %v): %w", readElapsed, totalElapsed, cfg.Timeout, ctx.Err())
	}
}

// parseResponse 解析API响应
func (cfg *Client) parseResponse(body []byte, statusCode int) (string, error) {
	// 检查HTTP状态码
	if statusCode != http.StatusOK {
		// 尝试解析错误响应
		var errorResp struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &errorResp); err == nil && errorResp.Error.Message != "" {
			return "", fmt.Errorf("API返回错误 (status %d): %s (类型: %s, 代码: %s)", 
				statusCode, errorResp.Error.Message, errorResp.Error.Type, errorResp.Error.Code)
		}
		return "", fmt.Errorf("API返回错误 (status %d): %s", statusCode, string(body))
	}

	// 解析响应
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w, 响应内容: %s", err, string(body))
	}

	// 检查是否有错误信息
	if result.Error.Message != "" {
		return "", fmt.Errorf("API返回错误: %s (类型: %s)", result.Error.Message, result.Error.Type)
	}

	if len(result.Choices) == 0 {
		// 记录完整响应以便调试
		responseStr := string(body)
		if len(responseStr) > 500 {
			responseStr = responseStr[:500] + "..."
		}
		return "", fmt.Errorf("API返回空响应 (没有choices)，完整响应: %s", responseStr)
	}

	// 检查是否被截断
	if result.Choices[0].FinishReason == "length" {
		fmt.Printf("⚠️  AI响应可能被截断 (finish_reason: length)，当前max_tokens可能不足\n")
	}
	
	// 记录token使用情况（用于调试）
	if result.Usage.TotalTokens > 0 {
		fmt.Printf("📊 AI Token使用: prompt=%d, completion=%d, total=%d\n", 
			result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)
	}

	content := result.Choices[0].Message.Content
	if content == "" {
		return "", fmt.Errorf("API返回的content为空，响应: %s", string(body))
	}

	return content, nil
}

// handleRequestError 处理请求错误
func (cfg *Client) handleRequestError(err error, elapsed time.Duration) error {
	if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline exceeded") {
		return fmt.Errorf("AI API请求超时 (已等待 %v，超时设置: %v): %w。可能原因：提示词过长、网络延迟、API服务器响应慢", elapsed, cfg.Timeout, err)
	}
	return fmt.Errorf("发送请求失败 (耗时 %v): %w", elapsed, err)
}

