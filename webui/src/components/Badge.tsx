import type { ReactNode } from 'react'
import clsx from 'clsx'
import type { CqsTier } from '../api/types'

interface BadgeProps {
  children: ReactNode
  variant?: 'default' | 'cyan' | 'emerald' | 'amber' | 'rose' | 'purple' | 'slate'
  size?: 'sm' | 'md'
  className?: string
}

export function Badge({ children, variant = 'default', size = 'sm', className }: BadgeProps) {
  const variantStyles = {
    default: 'bg-slate-800/80 text-slate-300 border-slate-700/60',
    cyan: 'bg-cyan-500/10 text-cyan-300 border-cyan-500/30',
    emerald: 'bg-emerald-500/10 text-emerald-300 border-emerald-500/30',
    amber: 'bg-amber-500/10 text-amber-300 border-amber-500/30',
    rose: 'bg-rose-500/10 text-rose-300 border-rose-500/30',
    purple: 'bg-purple-500/10 text-purple-300 border-purple-500/30',
    slate: 'bg-slate-900/60 text-slate-400 border-slate-800/60',
  }

  const sizeStyles = {
    sm: 'px-2 py-0.5 text-xs font-semibold',
    md: 'px-2.5 py-1 text-xs font-bold',
  }

  return (
    <span
      className={clsx(
        'inline-flex items-center gap-1.5 rounded-md border tracking-wide font-mono backdrop-blur-sm transition-colors',
        variantStyles[variant],
        sizeStyles[size],
        className,
      )}
    >
      {children}
    </span>
  )
}

export function CqsBadge({ score, tier }: { score: number; tier?: CqsTier }) {
  const resolvedTier = tier || (score >= 90 ? 'S' : score >= 80 ? 'A' : score >= 65 ? 'B' : score >= 50 ? 'C' : 'D')

  const tierColors: Record<CqsTier, BadgeProps['variant']> = {
    S: 'purple',
    A: 'emerald',
    B: 'cyan',
    C: 'amber',
    D: 'rose',
  }

  return (
    <Badge variant={tierColors[resolvedTier]} size="md">
      <span className="font-extrabold">CQS {score}</span>
      <span className="opacity-60">·</span>
      <span>Tier {resolvedTier}</span>
    </Badge>
  )
}

export function FormatBadge({ format }: { format: string }) {
  const fmt = format.toUpperCase().replace('.', '')
  const isEpub = fmt === 'EPUB'
  const isPdf = fmt === 'PDF'
  const isComic = fmt === 'CBZ' || fmt === 'CBR'

  const variant: BadgeProps['variant'] = isEpub ? 'cyan' : isPdf ? 'rose' : isComic ? 'purple' : 'slate'

  return <Badge variant={variant}>{fmt}</Badge>
}
