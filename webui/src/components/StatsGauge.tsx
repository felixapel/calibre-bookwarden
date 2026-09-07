interface StatsGaugeProps {
  value: number // 0 to 100
  label: string
  sublabel?: string
  size?: number
  strokeWidth?: number
}

export function StatsGauge({
  value,
  label,
  sublabel,
  size = 140,
  strokeWidth = 10,
}: StatsGaugeProps) {
  const clamped = Math.max(0, Math.min(100, value))
  const radius = (size - strokeWidth) / 2
  const circumference = 2 * Math.PI * radius
  const strokeDashoffset = circumference - (clamped / 100) * circumference

  // Color scheme based on score
  const strokeColor =
    clamped >= 85
      ? 'stroke-emerald-400'
      : clamped >= 70
        ? 'stroke-cyan-400'
        : clamped >= 50
          ? 'stroke-amber-400'
          : 'stroke-rose-400'

  const glowColor =
    clamped >= 85
      ? 'text-emerald-400'
      : clamped >= 70
        ? 'text-cyan-400'
        : clamped >= 50
          ? 'text-amber-400'
          : 'text-rose-400'

  return (
    <div className="flex flex-col items-center justify-center">
      <div className="relative flex items-center justify-center" style={{ width: size, height: size }}>
        <svg className="h-full w-full -rotate-90 transform" viewBox={`0 0 ${size} ${size}`}>
          {/* Background Track */}
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            className="stroke-slate-800"
            strokeWidth={strokeWidth}
            fill="transparent"
          />
          {/* Progress Arc */}
          <circle
            cx={size / 2}
            cy={size / 2}
            r={radius}
            className={`${strokeColor} transition-all duration-1000 ease-out`}
            strokeWidth={strokeWidth}
            strokeDasharray={circumference}
            strokeDashoffset={strokeDashoffset}
            strokeLinecap="round"
            fill="transparent"
          />
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center text-center">
          <span className={`text-3xl font-black tracking-tight font-mono ${glowColor}`}>
            {Math.round(clamped)}
          </span>
          <span className="text-[10px] font-bold uppercase tracking-widest text-slate-400">/ 100</span>
        </div>
      </div>
      <div className="mt-3 text-center">
        <p className="text-sm font-bold text-slate-200">{label}</p>
        {sublabel && <p className="text-xs text-slate-400 mt-0.5">{sublabel}</p>}
      </div>
    </div>
  )
}
