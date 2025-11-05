package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"backend/pkg/db"
	"time"
)

// DecisionStorage 决策记录存储（使用SQLite）
type DecisionStorage struct {
	dbManager *db.DBManager
	db        *sql.DB
}

// NewDecisionStorage 创建决策记录存储
func NewDecisionStorage(dbManager *db.DBManager) (*DecisionStorage, error) {
	storage := &DecisionStorage{
		dbManager: dbManager,
	}

	// 获取数据库连接
	database, err := dbManager.GetDB("decision_logs")
	if err != nil {
		return nil, fmt.Errorf("获取数据库连接失败: %w", err)
	}
	storage.db = database

	// 初始化表结构
	if err := storage.initTable(); err != nil {
		return nil, fmt.Errorf("初始化表结构失败: %w", err)
	}

	return storage, nil
}

// initTable 初始化表结构
func (s *DecisionStorage) initTable() error {
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS decisions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		trader_id TEXT NOT NULL,
		cycle_number INTEGER NOT NULL,
		timestamp DATETIME NOT NULL,
		input_prompt TEXT,
		cot_trace TEXT,
		decision_json TEXT,
		account_state TEXT,
		positions TEXT,
		candidate_coins TEXT,
		decisions TEXT,
		execution_log TEXT,
		success INTEGER NOT NULL DEFAULT 0,
		error_message TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	
	CREATE INDEX IF NOT EXISTS idx_trader_cycle ON decisions(trader_id, cycle_number);
	CREATE INDEX IF NOT EXISTS idx_timestamp ON decisions(timestamp);
	`

	_, err := s.db.Exec(createTableSQL)
	return err
}

// DecisionRecord 决策记录（与logger.DecisionRecord兼容）
type DecisionRecord struct {
	Timestamp      time.Time       `json:"timestamp"`
	CycleNumber    int             `json:"cycle_number"`
	InputPrompt    string          `json:"input_prompt"`
	CoTTrace       string          `json:"cot_trace"`
	DecisionJSON   string          `json:"decision_json"`
	AccountState   json.RawMessage `json:"account_state"`
	Positions      json.RawMessage `json:"positions"`
	CandidateCoins json.RawMessage `json:"candidate_coins"`
	Decisions      json.RawMessage `json:"decisions"`
	ExecutionLog   json.RawMessage `json:"execution_log"`
	Success        bool            `json:"success"`
	ErrorMessage   string          `json:"error_message"`
}

// LogDecision 记录决策
func (s *DecisionStorage) LogDecision(traderID string, record *DecisionRecord) error {
	// 序列化各个字段
	accountStateJSON, _ := json.Marshal(record.AccountState)
	positionsJSON, _ := json.Marshal(record.Positions)
	candidateCoinsJSON, _ := json.Marshal(record.CandidateCoins)
	decisionsJSON, _ := json.Marshal(record.Decisions)
	executionLogJSON, _ := json.Marshal(record.ExecutionLog)

	success := 0
	if record.Success {
		success = 1
	}

	query := `
		INSERT INTO decisions (
			trader_id, cycle_number, timestamp, input_prompt, cot_trace,
			decision_json, account_state, positions, candidate_coins,
			decisions, execution_log, success, error_message
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.Exec(query,
		traderID, record.CycleNumber, record.Timestamp,
		record.InputPrompt, record.CoTTrace, record.DecisionJSON,
		string(accountStateJSON), string(positionsJSON),
		string(candidateCoinsJSON), string(decisionsJSON),
		string(executionLogJSON), success, record.ErrorMessage,
	)

	if err != nil {
		return fmt.Errorf("保存决策记录失败: %w", err)
	}

	return nil
}

// GetLatestRecords 获取最近N条记录（按时间逆序：从新到旧）
func (s *DecisionStorage) GetLatestRecords(traderID string, n int) ([]*DecisionRecord, error) {
	query := `
		SELECT cycle_number, timestamp, input_prompt, cot_trace, decision_json,
		       account_state, positions, candidate_coins, decisions, execution_log,
		       success, error_message
		FROM decisions
		WHERE trader_id = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`

	rows, err := s.db.Query(query, traderID, n)
	if err != nil {
		return nil, fmt.Errorf("查询决策记录失败: %w", err)
	}
	defer rows.Close()

	var records []*DecisionRecord
	for rows.Next() {
		record := &DecisionRecord{}
		var success int
		var accountStateJSON, positionsJSON, candidateCoinsJSON, decisionsJSON, executionLogJSON string

		err := rows.Scan(
			&record.CycleNumber, &record.Timestamp, &record.InputPrompt,
			&record.CoTTrace, &record.DecisionJSON,
			&accountStateJSON, &positionsJSON, &candidateCoinsJSON,
			&decisionsJSON, &executionLogJSON,
			&success, &record.ErrorMessage,
		)

		if err != nil {
			log.Printf("⚠️  扫描决策记录失败: %v", err)
			continue
		}

		record.Success = success == 1
		record.AccountState = json.RawMessage(accountStateJSON)
		record.Positions = json.RawMessage(positionsJSON)
		record.CandidateCoins = json.RawMessage(candidateCoinsJSON)
		record.Decisions = json.RawMessage(decisionsJSON)
		record.ExecutionLog = json.RawMessage(executionLogJSON)

		records = append(records, record)
	}

	if err := rows.Err(); err != nil {
		log.Printf("⚠️  查询决策记录时出现行扫描错误: %v", err)
		return records, nil // 返回已收集的记录而不是错误
	}

	return records, nil
}

// GetForcedCloses 获取最近的强制平仓记录
func (s *DecisionStorage) GetForcedCloses(traderID string, maxCycles int) ([]string, error) {
	records, err := s.GetLatestRecords(traderID, maxCycles)
	if err != nil {
		return nil, err
	}

	// 需要导入logger包来使用DecisionAction类型
	// 由于无法直接导入，我们使用map[string]interface{}来解析
	var forcedCloses []string
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		
		// 解析decisions字段为通用的map结构
		var decisions []map[string]interface{}
		if err := json.Unmarshal(record.Decisions, &decisions); err != nil {
			log.Printf("⚠️  解析决策记录失败 (周期 #%d): %v", record.CycleNumber, err)
			continue
		}

		for _, actionMap := range decisions {
			// 从map中提取字段
			isForcedVal, ok := actionMap["is_forced"]
			if !ok {
				continue
			}
			isForced, ok := isForcedVal.(bool)
			if !ok {
				// 尝试从数字转换（数据库可能存储为0/1）
				if isForcedNum, ok := isForcedVal.(float64); ok {
					isForced = isForcedNum != 0
				} else {
					continue
				}
			}

			actionStr, _ := actionMap["action"].(string)
			symbol, _ := actionMap["symbol"].(string)
			forcedReason, _ := actionMap["forced_reason"].(string)

			if isForced && (actionStr == "close_long" || actionStr == "close_short") {
				cycleNum := record.CycleNumber
				forcedCloses = append(forcedCloses, fmt.Sprintf("%s: %s %s - %s (周期 #%d)",
					record.Timestamp.Format("15:04:05"), symbol, actionStr, forcedReason, cycleNum))
			}
		}
	}

	return forcedCloses, nil
}

