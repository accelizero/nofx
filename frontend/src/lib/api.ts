import type {
  SystemStatus,
  AccountInfo,
  Position,
  DecisionRecord,
  Statistics,
  TraderInfo,
  CompetitionData,
} from '../types';

const API_BASE = '/api';

// 通用的错误处理函数
async function handleResponse<T>(res: Response, defaultError: string): Promise<T> {
  if (!res.ok) {
    // 尝试获取错误信息
    let errorMessage = defaultError;
    try {
      const errorData = await res.json();
      if (errorData.error || errorData.message) {
        errorMessage = errorData.error || errorData.message;
      }
    } catch {
      // 如果不是JSON，使用状态码信息
      errorMessage = `${defaultError} (HTTP ${res.status})`;
    }
    throw new Error(errorMessage);
  }
  
  try {
    return await res.json();
  } catch (error) {
    // JSON解析错误
    throw new Error(`${defaultError}：响应格式错误`);
  }
}

// 通用的网络请求包装函数
async function safeFetch<T>(
  url: string,
  defaultError: string,
  options?: RequestInit
): Promise<T> {
  try {
    const res = await fetch(url, options);
    return await handleResponse<T>(res, defaultError);
  } catch (error) {
    // 网络错误或其他错误
    if (error instanceof TypeError && error.message.includes('fetch')) {
      throw new Error('网络连接失败，请检查网络连接');
    }
    // 重新抛出其他错误
    throw error;
  }
}

export const api = {
  // 竞赛相关接口
  async getCompetition(): Promise<CompetitionData> {
    return safeFetch<CompetitionData>(
      `${API_BASE}/competition`,
      '获取竞赛数据失败'
    );
  },

  async getTraders(): Promise<TraderInfo[]> {
    return safeFetch<TraderInfo[]>(
      `${API_BASE}/traders`,
      '获取trader列表失败'
    );
  },

  // 获取系统状态（支持trader_id）
  async getStatus(traderId?: string): Promise<SystemStatus> {
    const url = traderId
      ? `${API_BASE}/status?trader_id=${traderId}`
      : `${API_BASE}/status`;
    return safeFetch<SystemStatus>(url, '获取系统状态失败');
  },

  // 获取账户信息（支持trader_id）
  async getAccount(traderId?: string): Promise<AccountInfo> {
    const url = traderId
      ? `${API_BASE}/account?trader_id=${traderId}`
      : `${API_BASE}/account`;
    const data = await safeFetch<AccountInfo>(
      url,
      '获取账户信息失败',
      {
        cache: 'no-store',
        headers: {
          'Cache-Control': 'no-cache',
        },
      }
    );
    console.log('Account data fetched:', data);
    return data;
  },

  // 获取持仓列表（支持trader_id）
  async getPositions(traderId?: string): Promise<Position[]> {
    const url = traderId
      ? `${API_BASE}/positions?trader_id=${traderId}`
      : `${API_BASE}/positions`;
    return safeFetch<Position[]>(url, '获取持仓列表失败');
  },

  // 获取决策日志（支持trader_id）
  async getDecisions(traderId?: string): Promise<DecisionRecord[]> {
    const url = traderId
      ? `${API_BASE}/decisions?trader_id=${traderId}`
      : `${API_BASE}/decisions`;
    return safeFetch<DecisionRecord[]>(url, '获取决策日志失败');
  },

  // 获取最新决策（支持trader_id）
  async getLatestDecisions(traderId?: string): Promise<DecisionRecord[]> {
    const url = traderId
      ? `${API_BASE}/decisions/latest?trader_id=${traderId}`
      : `${API_BASE}/decisions/latest`;
    return safeFetch<DecisionRecord[]>(url, '获取最新决策失败');
  },

  // 获取统计信息（支持trader_id）
  async getStatistics(traderId?: string): Promise<Statistics> {
    const url = traderId
      ? `${API_BASE}/statistics?trader_id=${traderId}`
      : `${API_BASE}/statistics`;
    return safeFetch<Statistics>(url, '获取统计信息失败');
  },

  // 获取收益率历史数据（支持trader_id）
  async getEquityHistory(traderId?: string): Promise<any[]> {
    const url = traderId
      ? `${API_BASE}/equity-history?trader_id=${traderId}`
      : `${API_BASE}/equity-history`;
    return safeFetch<any[]>(url, '获取历史数据失败');
  },

  // 获取AI学习表现分析（支持trader_id）
  async getPerformance(traderId?: string): Promise<any> {
    const url = traderId
      ? `${API_BASE}/performance?trader_id=${traderId}`
      : `${API_BASE}/performance`;
    return safeFetch<any>(url, '获取AI学习数据失败');
  },
};
