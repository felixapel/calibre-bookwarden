import type { ReactNode } from 'react'
import clsx from 'clsx'

interface BentoCardProps {
  children: ReactNode
  title?: string
  subtitle?: string
  icon?: ReactNode
  badge?: ReactNode
  action?: ReactNode
  className?: string
  glow?: boolean
}

export function BentoCard({
  children,
  title,
  subtitle,
  icon,
  badge,
  action,
  className,
  glow = false,
}: BentoCardProps) {
  return (
    <div
      className={clsx(
        'group relative flex flex-col rounded-2xl border border-slate-800/80 bg-gradient-to-b from-slate-900/80 to-slate-950/90 p-6 backdrop-blur-xl transition-all duration-300',
        'hover:border-slate-700/80 hover:shadow-2xl hover:shadow-cyan-950/20',
        glow && 'ring-1 ring-cyan-500/20 shadow-lg shadow-cyan-950/30',
        className,
      )}
    >
      {(title || icon || action || badge) && (
        <div className="mb-5 flex items-start justify-between gap-4">
          <div className="flex items-center gap-3">
            {icon && (
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border border-slate-700/60 bg-slate-800/60 text-cyan-400 shadow-inner group-hover:border-cyan-500/40 group-hover:text-cyan-300 transition-colors">
                {icon}
              </div>
            )}
            <div>
              <div className="flex items-center gap-2.5">
                {title && <h3 className="text-base font-bold tracking-tight text-slate-100">{title}</h3>}
                {badge}
              </div>
              {subtitle && <p className="text-xs text-slate-400 mt-0.5">{subtitle}</p>}
            </div>
          </div>
          {action && <div className="shrink-0">{action}</div>}
        </div>
      )}
      <div className="flex-1">{children}</div>
    </div>
  )
}
