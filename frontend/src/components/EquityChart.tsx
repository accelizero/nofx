import { useState } from 'react';
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ReferenceLine,
} from 'recharts';
import useSWR from 'swr';
import { api } from '../lib/api';
import { useLanguage } from '../contexts/LanguageContext';
import { t } from '../i18n/translations';

interface EquityPoint {
  timestamp: string;
  total_equity: number;
  total_pnl: number;
  total_pnl_pct: number;
  initial_balance: number; // 初始余额（从后端API获取，确保一致性）
  cycle_number: number;
}

interface EquityChartProps {
  traderId?: string;
}

export function EquityChart({ traderId }: EquityChartProps) {
  const { language } = useLanguage();
  const [displayMode, setDisplayMode] = useState<'dollar' | 'percent'>('percent');

  const { data: history, error } = useSWR<EquityPoint[]>(
    traderId ? `equity-history-${traderId}` : 'equity-history',
    () => api.getEquityHistory(traderId),
    {
      refreshInterval: 30000, // 30秒刷新（历史数据更新频率较低）
      revalidateOnFocus: false,
      dedupingInterval: 20000,
    }
  );

  const { data: account } = useSWR(
    traderId ? `account-${traderId}` : 'account',
    () => api.getAccount(traderId),
    {
      refreshInterval: 15000, // 15秒刷新（配合后端缓存）
      revalidateOnFocus: false,
      dedupingInterval: 10000,
    }
  );

  if (error) {
    return (
      <div className="binance-card p-6">
        <div className="flex items-center gap-3 p-4 rounded" style={{ background: 'rgba(246, 70, 93, 0.1)', border: '1px solid rgba(246, 70, 93, 0.2)' }}>
          <div className="text-2xl">⚠️</div>
          <div>
            <div className="font-semibold" style={{ color: '#F6465D' }}>{t('loadingError', language)}</div>
            <div className="text-sm" style={{ color: '#848E9C' }}>{error.message}</div>
          </div>
        </div>
      </div>
    );
  }

  // 过滤掉无效数据：total_equity为0或小于1的数据点（API失败导致）
  const validHistory = history?.filter(point => point.total_equity > 1) || [];

  if (!validHistory || validHistory.length === 0) {
    return (
      <div className="binance-card p-6">
        <h3 className="text-lg font-semibold mb-6" style={{ color: '#EAECEF' }}>{t('accountEquityCurve', language)}</h3>
        <div className="text-center py-16" style={{ color: '#848E9C' }}>
          <div className="text-6xl mb-4 opacity-50">📊</div>
          <div className="text-lg font-semibold mb-2">{t('noHistoricalData', language)}</div>
          <div className="text-sm">{t('dataWillAppear', language)}</div>
        </div>
      </div>
    );
  }

  // 限制显示最近的数据点（性能优化）
  // 如果数据超过2000个点，只显示最近2000个
  const MAX_DISPLAY_POINTS = 2000;
  const displayHistory = validHistory.length > MAX_DISPLAY_POINTS
    ? validHistory.slice(-MAX_DISPLAY_POINTS)
    : validHistory;

  // 使用后端API返回的initial_balance（确保与后端计算一致）
  // 优先使用历史数据中的initial_balance，如果没有则从account获取，最后才用默认值
  const initialBalance = validHistory[0]?.initial_balance
    || account?.initial_balance
    || validHistory[0]?.total_equity  // fallback：使用第一个有效数据点
    || account?.total_equity
    || 100;  // 最后的默认值

  // 转换数据格式 - 直接使用后端API返回的值，避免重复计算
  const chartData = displayHistory.map((point) => {
    // 使用后端API返回的total_pnl和total_pnl_pct，确保数据一致性
    const pnl = point.total_pnl ?? (point.total_equity - initialBalance);
    const pnlPct = point.total_pnl_pct ?? ((pnl / initialBalance) * 100);
    
    return {
      time: new Date(point.timestamp).toLocaleTimeString('zh-CN', {
        hour: '2-digit',
        minute: '2-digit',
      }),
      value: displayMode === 'dollar' ? point.total_equity : pnlPct,
      cycle: point.cycle_number,
      raw_equity: point.total_equity,
      raw_pnl: pnl,
      raw_pnl_pct: pnlPct,
    };
  });

  const currentValue = chartData[chartData.length - 1];
  const isProfit = currentValue.raw_pnl >= 0;

  // 计算Y轴范围
  const calculateYDomain = () => {
    if (displayMode === 'percent') {
      // 百分比模式：找到最大最小值，留20%余量
      const values = chartData.map(d => d.value);
      const minVal = Math.min(...values);
      const maxVal = Math.max(...values);
      const range = Math.max(Math.abs(maxVal), Math.abs(minVal));
      const padding = Math.max(range * 0.2, 1); // 至少留1%余量
      return [Math.floor(minVal - padding), Math.ceil(maxVal + padding)];
    } else {
      // 美元模式：以初始余额为基准，上下留10%余量
      const values = chartData.map(d => d.value);
      const minVal = Math.min(...values, initialBalance);
      const maxVal = Math.max(...values, initialBalance);
      const range = maxVal - minVal;
      const padding = Math.max(range * 0.15, initialBalance * 0.01); // 至少留1%余量
      return [
        Math.floor(minVal - padding),
        Math.ceil(maxVal + padding)
      ];
    }
  };

  // 自定义Tooltip - Binance Style
  const CustomTooltip = ({ active, payload }: any) => {
    if (active && payload && payload.length) {
      const data = payload[0].payload;
      return (
        <div className="rounded p-3 shadow-xl" style={{ background: '#1E2329', border: '1px solid #2B3139' }}>
          <div className="text-xs mb-1" style={{ color: '#848E9C' }}>Cycle #{data.cycle}</div>
          <div className="font-bold mono" style={{ color: '#EAECEF' }}>
            {data.raw_equity.toFixed(2)} USDT
          </div>
          <div
            className="text-sm mono font-bold"
            style={{ color: data.raw_pnl >= 0 ? '#0ECB81' : '#F6465D' }}
          >
            {data.raw_pnl >= 0 ? '+' : ''}
            {data.raw_pnl.toFixed(2)} USDT ({data.raw_pnl_pct >= 0 ? '+' : ''}
            {data.raw_pnl_pct.toFixed(2)}%)
          </div>
        </div>
      );
    }
    return null;
  };

  return (
    <div className="binance-card p-3 sm:p-5 animate-fade-in">
      {/* Header */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between mb-4">
        <div className="flex-1">
          <h3 className="text-base sm:text-lg font-bold mb-2" style={{ color: '#EAECEF' }}>{t('accountEquityCurve', language)}</h3>
          <div className="flex flex-col sm:flex-row sm:items-baseline gap-2 sm:gap-4">
            <span className="text-2xl sm:text-3xl font-bold mono" style={{ color: '#EAECEF' }}>
              {account?.total_equity.toFixed(2) || '0.00'}
              <span className="text-base sm:text-lg ml-1" style={{ color: '#848E9C' }}>USDT</span>
            </span>
            <div className="flex items-center gap-2 flex-wrap">
              <span
                className="text-sm sm:text-lg font-bold mono px-2 sm:px-3 py-1 rounded"
                style={{
                  color: isProfit ? '#0ECB81' : '#F6465D',
                  background: isProfit ? 'rgba(14, 203, 129, 0.1)' : 'rgba(246, 70, 93, 0.1)',
                  border: `1px solid ${isProfit ? 'rgba(14, 203, 129, 0.2)' : 'rgba(246, 70, 93, 0.2)'}`
                }}
              >
                {isProfit ? '▲' : '▼'} {isProfit ? '+' : ''}
                {currentValue.raw_pnl_pct.toFixed(2)}%
              </span>
              <span className="text-xs sm:text-sm mono" style={{ color: '#848E9C' }}>
                ({isProfit ? '+' : ''}{currentValue.raw_pnl.toFixed(2)} USDT)
              </span>
            </div>
          </div>
        </div>

        {/* Display Mode Toggle */}
        <div className="flex gap-0.5 sm:gap-1 rounded p-0.5 sm:p-1 self-start sm:self-auto" style={{ background: '#0B0E11', border: '1px solid #2B3139' }}>
          <button
            onClick={() => setDisplayMode('dollar')}
            className="px-3 sm:px-4 py-1.5 sm:py-2 rounded text-xs sm:text-sm font-bold transition-all"
            style={displayMode === 'dollar'
              ? { background: '#F0B90B', color: '#000', boxShadow: '0 2px 8px rgba(240, 185, 11, 0.4)' }
              : { background: 'transparent', color: '#848E9C' }
            }
          >
            💵 USDT
          </button>
          <button
            onClick={() => setDisplayMode('percent')}
            className="px-3 sm:px-4 py-1.5 sm:py-2 rounded text-xs sm:text-sm font-bold transition-all"
            style={displayMode === 'percent'
              ? { background: '#F0B90B', color: '#000', boxShadow: '0 2px 8px rgba(240, 185, 11, 0.4)' }
              : { background: 'transparent', color: '#848E9C' }
            }
          >
            📊 %
          </button>
        </div>
      </div>

      {/* Chart */}
      <div className="my-2" style={{ borderRadius: '8px', overflow: 'hidden' }}>
        <ResponsiveContainer width="100%" height={280}>
        <LineChart data={chartData} margin={{ top: 10, right: 20, left: 5, bottom: 30 }}>
          <defs>
            <linearGradient id="colorGradient" x1="0" y1="0" x2="0" y2="1">
              <stop offset="5%" stopColor="#F0B90B" stopOpacity={0.8} />
              <stop offset="95%" stopColor="#FCD535" stopOpacity={0.2} />
            </linearGradient>
          </defs>
          <CartesianGrid strokeDasharray="3 3" stroke="#2B3139" />
          <XAxis
            dataKey="time"
            stroke="#5E6673"
            tick={{ fill: '#848E9C', fontSize: 11 }}
            tickLine={{ stroke: '#2B3139' }}
            interval={Math.floor(chartData.length / 10)}
            angle={-15}
            textAnchor="end"
            height={60}
          />
          <YAxis
            stroke="#5E6673"
            tick={{ fill: '#848E9C', fontSize: 12 }}
            tickLine={{ stroke: '#2B3139' }}
            domain={calculateYDomain()}
            tickFormatter={(value) =>
              displayMode === 'dollar' ? `$${value.toFixed(0)}` : `${value.toFixed(2)}%`
            }
          />
          <Tooltip content={<CustomTooltip />} />
          <ReferenceLine
            y={displayMode === 'dollar' ? initialBalance : 0}
            stroke="#474D57"
            strokeDasharray="3 3"
            label={{
              value: displayMode === 'dollar' ? t('initialBalance', language).split(' ')[0] : '0%',
              fill: '#848E9C',
              fontSize: 12,
            }}
          />
          <Line
            type="natural"
            dataKey="value"
            stroke="url(#colorGradient)"
            strokeWidth={3}
            dot={chartData.length > 50 ? false : { fill: '#F0B90B', r: 3 }}
            activeDot={{ r: 6, fill: '#FCD535', stroke: '#F0B90B', strokeWidth: 2 }}
            connectNulls={true}
          />
        </LineChart>
      </ResponsiveContainer>
      </div>

      {/* Footer Stats */}
      <div className="mt-3 grid grid-cols-2 sm:grid-cols-4 gap-2 sm:gap-3 pt-3" style={{ borderTop: '1px solid #2B3139' }}>
        <div className="p-2 rounded transition-all hover:bg-opacity-50" style={{ background: 'rgba(240, 185, 11, 0.05)' }}>
          <div className="text-xs mb-1 uppercase tracking-wider" style={{ color: '#848E9C' }}>{t('initialBalance', language)}</div>
          <div className="text-xs sm:text-sm font-bold mono" style={{ color: '#EAECEF' }}>
            {initialBalance.toFixed(2)} USDT
          </div>
        </div>
        <div className="p-2 rounded transition-all hover:bg-opacity-50" style={{ background: 'rgba(240, 185, 11, 0.05)' }}>
          <div className="text-xs mb-1 uppercase tracking-wider" style={{ color: '#848E9C' }}>{t('currentEquity', language)}</div>
          <div className="text-xs sm:text-sm font-bold mono" style={{ color: '#EAECEF' }}>
            {currentValue.raw_equity.toFixed(2)} USDT
          </div>
        </div>
        <div className="p-2 rounded transition-all hover:bg-opacity-50" style={{ background: 'rgba(240, 185, 11, 0.05)' }}>
          <div className="text-xs mb-1 uppercase tracking-wider" style={{ color: '#848E9C' }}>{t('historicalCycles', language)}</div>
          <div className="text-xs sm:text-sm font-bold mono" style={{ color: '#EAECEF' }}>{validHistory.length} {t('cycles', language)}</div>
        </div>
        <div className="p-2 rounded transition-all hover:bg-opacity-50" style={{ background: 'rgba(240, 185, 11, 0.05)' }}>
          <div className="text-xs mb-1 uppercase tracking-wider" style={{ color: '#848E9C' }}>{t('displayRange', language)}</div>
          <div className="text-xs sm:text-sm font-bold mono" style={{ color: '#EAECEF' }}>
            {validHistory.length > MAX_DISPLAY_POINTS
              ? `${t('recent', language)} ${MAX_DISPLAY_POINTS}`
              : t('allData', language)
            }
          </div>
        </div>
      </div>
    </div>
  );
}
