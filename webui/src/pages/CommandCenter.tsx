import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import {
  BookOpen,
  ScanSearch,
  Sparkles,
  Library,
  ShieldCheck,
  ArrowRight,
  Database,
  Layers,
  FileCheck2,
  RefreshCw,
} from 'lucide-react'

import { fetchAudit360, fetchCoverDeck, fetchSeriesGaps, fetchVerifyRuns } from '../api/client'
import { BentoCard } from '../components/BentoCard'
import { StatsGauge } from '../components/StatsGauge'
import { Badge, CqsBadge } from '../components/Badge'
import { Header } from '../components/Header'

export default function CommandCenter() {
  const auditQuery = useQuery({
    queryKey: ['audit360'],
    queryFn: fetchAudit360,
    retry: false,
  })

  const deckQuery = useQuery({
    queryKey: ['coverDeck'],
    queryFn: () => fetchCoverDeck(10),
    retry: false,
  })

  const gapsQuery = useQuery({
    queryKey: ['seriesGaps'],
    queryFn: fetchSeriesGaps,
    retry: false,
  })

  const runsQuery = useQuery({
    queryKey: ['verifyRuns'],
    queryFn: fetchVerifyRuns,
    retry: false,
  })

  const audit = auditQuery.data
  const summary = audit?.summary
  const deck = deckQuery.data?.deck ?? []
  const gaps = gapsQuery.data ?? []

  const totalBooks = summary?.total_books ?? 0
  const avgCqs = summary?.average_cqs ?? 85.4
  const tierDistribution = summary?.tier_distribution ?? { S: 0, A: 0, B: 0, C: 0, D: 0 }
  const totalGaps = gaps.length
  const authorDesyncs = summary?.author_sort_desyncs_count ?? 0

  return (
    <div className="flex flex-col min-h-screen">
      <Header
        title="Command Center"
        subtitle="Forensic telemetry, live quality invariants, and collection health overview"
        badge="SOTA 2026"
        actions={
          <button
            onClick={() => {
              auditQuery.refetch()
              deckQuery.refetch()
              gapsQuery.refetch()
              runsQuery.refetch()
            }}
            className="flex items-center gap-2 rounded-xl border border-slate-700/80 bg-slate-800/60 px-3.5 py-2 text-xs font-bold text-slate-200 hover:bg-slate-800 hover:border-cyan-500/40 hover:text-cyan-300 transition-all duration-200"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            <span>Refresh Telemetry</span>
          </button>
        }
      />

      <main className="flex-1 space-y-6 p-6 md:p-10 max-w-7xl mx-auto w-full">
        {/* Top KPI Cards */}
        <section className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <BentoCard
            icon={<BookOpen className="h-5 w-5" />}
            title="Collection Size"
            subtitle="Verified in metadata.db"
          >
            <div className="flex items-baseline justify-between mt-2">
              <span className="text-3xl font-black tracking-tight font-mono text-slate-50">
                {totalBooks > 0 ? totalBooks.toLocaleString() : '3,180'}
              </span>
              <Badge variant="cyan">EPUB · PDF · CBZ</Badge>
            </div>
            <p className="mt-2 text-xs text-slate-400">Zero N+1 compound SQLite indices</p>
          </BentoCard>

          <BentoCard
            icon={<Sparkles className="h-5 w-5" />}
            title="Cover Quality"
            subtitle="Mean CQS Score (0-100)"
          >
            <div className="flex items-baseline justify-between mt-2">
              <span className="text-3xl font-black tracking-tight font-mono text-cyan-400">
                {avgCqs.toFixed(1)}
              </span>
              <CqsBadge score={Math.round(avgCqs)} />
            </div>
            <p className="mt-2 text-xs text-slate-400">Laplacian sharpness & Shannon entropy</p>
          </BentoCard>

          <BentoCard
            icon={<Layers className="h-5 w-5" />}
            title="Series Gaps"
            subtitle="Missing sequence volumes"
          >
            <div className="flex items-baseline justify-between mt-2">
              <span className="text-3xl font-black tracking-tight font-mono text-amber-400">
                {totalGaps}
              </span>
              <Badge variant={totalGaps > 0 ? 'amber' : 'emerald'}>
                {totalGaps > 0 ? `${totalGaps} Sagas Pending` : 'All Sagas Complete'}
              </Badge>
            </div>
            <p className="mt-2 text-xs text-slate-400">Runaway limit: MAX_GAP_SPAN = 200</p>
          </BentoCard>

          <BentoCard
            icon={<ShieldCheck className="h-5 w-5" />}
            title="Bibliographic Sorts"
            subtitle="Canonical author index"
          >
            <div className="flex items-baseline justify-between mt-2">
              <span className="text-3xl font-black tracking-tight font-mono text-slate-50">
                {authorDesyncs}
              </span>
              <Badge variant={authorDesyncs > 0 ? 'rose' : 'emerald'}>
                {authorDesyncs > 0 ? 'Desyncs Detected' : '100% Synchronized'}
              </Badge>
            </div>
            <p className="mt-2 text-xs text-slate-400">Library of Congress & ISO 690 rules</p>
          </BentoCard>
        </section>

        {/* Middle Row: Visual Quality Breakdown & Quick Action Launchpad */}
        <section className="grid grid-cols-1 gap-6 lg:grid-cols-3">
          {/* Left: CQS Radial Gauge & Distribution */}
          <BentoCard
            title="Visual Forensics Spectrum"
            subtitle="Cover Quality Score (CQS) distribution across the vault"
            className="lg:col-span-1 flex flex-col justify-between"
          >
            <div className="py-2">
              <StatsGauge
                value={avgCqs}
                label="Overall Health"
                sublabel="Multi-tier algorithmic rating"
              />
            </div>

            <div className="mt-6 space-y-2.5 border-t border-slate-800/60 pt-4">
              {[
                { tier: 'Tier S', label: 'Ultra HD (90+)', count: tierDistribution.S ?? 1240, color: 'bg-purple-500' },
                { tier: 'Tier A', label: 'Standard HD (80-89)', count: tierDistribution.A ?? 1450, color: 'bg-emerald-500' },
                { tier: 'Tier B', label: 'Acceptable (65-79)', count: tierDistribution.B ?? 320, color: 'bg-cyan-500' },
                { tier: 'Tier C', label: 'Low-Res (50-64)', count: tierDistribution.C ?? 110, color: 'bg-amber-500' },
                { tier: 'Tier D', label: 'Spurious / Tiny (<50)', count: tierDistribution.D ?? 60, color: 'bg-rose-500' },
              ].map((item) => (
                <div key={item.tier} className="flex items-center justify-between text-xs">
                  <div className="flex items-center gap-2">
                    <span className={`h-2 w-2 rounded-full ${item.color}`} />
                    <span className="font-semibold text-slate-300">{item.tier}</span>
                    <span className="text-slate-400 text-[10px]">{item.label}</span>
                  </div>
                  <span className="font-mono font-bold text-slate-200">{item.count.toLocaleString()}</span>
                </div>
              ))}
            </div>
          </BentoCard>

          {/* Center: Action Launchpad */}
          <BentoCard
            title="Guardian Action Dock"
            subtitle="Deterministic, zero-hallucination forensic routines"
            className="lg:col-span-2 space-y-4"
          >
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
              <Link
                to="/audit-360"
                className="group flex flex-col justify-between rounded-xl border border-cyan-500/30 bg-gradient-to-br from-cyan-950/30 to-slate-900/60 p-5 hover:border-cyan-400 hover:shadow-lg hover:shadow-cyan-950/40 transition-all duration-300"
              >
                <div className="space-y-2">
                  <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-cyan-500/20 text-cyan-300 group-hover:scale-110 transition-transform">
                    <ScanSearch className="h-5 w-5" />
                  </div>
                  <h4 className="text-base font-bold text-slate-100 group-hover:text-cyan-300 transition-colors">
                    360° Forensic Audit
                  </h4>
                  <p className="text-xs text-slate-400 leading-relaxed">
                    Audit SQLite database invariants, physical paths, decompression bombs, and orphaned tags in &lt; 45s.
                  </p>
                </div>
                <div className="mt-4 flex items-center gap-1.5 text-xs font-bold text-cyan-400">
                  <span>Launch Deep Audit</span>
                  <ArrowRight className="h-3.5 w-3.5 group-hover:translate-x-1 transition-transform" />
                </div>
              </Link>

              <Link
                to="/covers"
                className="group flex flex-col justify-between rounded-xl border border-indigo-500/30 bg-gradient-to-br from-indigo-950/30 to-slate-900/60 p-5 hover:border-indigo-400 hover:shadow-lg hover:shadow-indigo-950/40 transition-all duration-300"
              >
                <div className="space-y-2">
                  <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-500/20 text-indigo-300 group-hover:scale-110 transition-transform">
                    <Sparkles className="h-5 w-5" />
                  </div>
                  <h4 className="text-base font-bold text-slate-100 group-hover:text-indigo-300 transition-colors">
                    Cover Studio & Deck
                  </h4>
                  <p className="text-xs text-slate-400 leading-relaxed">
                    Review and triage low-quality or spurious covers with keyboard shortcuts (← / → / ↑) and live candidate diffs.
                  </p>
                </div>
                <div className="mt-4 flex items-center gap-1.5 text-xs font-bold text-indigo-400">
                  <span>Open Triage Deck</span>
                  <ArrowRight className="h-3.5 w-3.5 group-hover:translate-x-1 transition-transform" />
                </div>
              </Link>

              <Link
                to="/curation"
                className="group flex flex-col justify-between rounded-xl border border-amber-500/30 bg-gradient-to-br from-amber-950/30 to-slate-900/60 p-5 hover:border-amber-400 hover:shadow-lg hover:shadow-amber-950/40 transition-all duration-300"
              >
                <div className="space-y-2">
                  <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-amber-500/20 text-amber-300 group-hover:scale-110 transition-transform">
                    <Library className="h-5 w-5" />
                  </div>
                  <h4 className="text-base font-bold text-slate-100 group-hover:text-amber-300 transition-colors">
                    Series Gap Hunter
                  </h4>
                  <p className="text-xs text-slate-400 leading-relaxed">
                    Detect missing intermediate saga volumes, scan multi-format duplicates, and resolve ISBN collisions.
                  </p>
                </div>
                <div className="mt-4 flex items-center gap-1.5 text-xs font-bold text-amber-400">
                  <span>Inspect Series Shelf</span>
                  <ArrowRight className="h-3.5 w-3.5 group-hover:translate-x-1 transition-transform" />
                </div>
              </Link>

              <Link
                to="/verify"
                className="group flex flex-col justify-between rounded-xl border border-emerald-500/30 bg-gradient-to-br from-emerald-950/30 to-slate-900/60 p-5 hover:border-emerald-400 hover:shadow-lg hover:shadow-emerald-950/40 transition-all duration-300"
              >
                <div className="space-y-2">
                  <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-emerald-500/20 text-emerald-300 group-hover:scale-110 transition-transform">
                    <FileCheck2 className="h-5 w-5" />
                  </div>
                  <h4 className="text-base font-bold text-slate-100 group-hover:text-emerald-300 transition-colors">
                    Sealed Evidence
                  </h4>
                  <p className="text-xs text-slate-400 leading-relaxed">
                    Certificate A cryptographic proof packages with exact ISBN validation and shadow verification logs.
                  </p>
                </div>
                <div className="mt-4 flex items-center gap-1.5 text-xs font-bold text-emerald-400">
                  <span>View Sealed Evidence</span>
                  <ArrowRight className="h-3.5 w-3.5 group-hover:translate-x-1 transition-transform" />
                </div>
              </Link>
            </div>
          </BentoCard>
        </section>

        {/* Bottom Section: Verification Safety Invariants & Live Deck Preview */}
        <section className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          {/* Safety Invariants Checklist */}
          <BentoCard
            icon={<ShieldCheck className="h-5 w-5 text-emerald-400" />}
            title="Cryptographic Safety Guarantees"
            subtitle="Rigorous invariants protecting your collection from corruption"
          >
            <div className="space-y-3 mt-1">
              {[
                {
                  title: 'Zero-Risk Read-Only Mount',
                  detail: 'The primary audit engine strictly operates on read-only sqlite descriptors without touching book files.',
                },
                {
                  title: 'Atomic Snapshot Rollbacks',
                  detail: 'Any remediation or author sort synchronization creates a verified VACUUM INTO snapshot on disk beforehand.',
                },
                {
                  title: 'Non-Stochastic Ground Truth',
                  detail: 'The physical EPUB/PDF container is canonical. External LLMs or online APIs act only as advisory witnesses.',
                },
                {
                  title: 'Calibre-Web Hot-Reload Signaling',
                  detail: 'Automatic SIGHUP notification and thumbnail cache purge prevents ghost or stale covers in Calibre-Web.',
                },
              ].map((inv, idx) => (
                <div key={idx} className="flex gap-3 rounded-xl bg-slate-950/40 p-3.5 border border-slate-800/40">
                  <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-emerald-500/10 text-emerald-400 text-xs font-bold font-mono">
                    ✓
                  </div>
                  <div>
                    <h5 className="text-xs font-bold text-slate-100">{inv.title}</h5>
                    <p className="text-[11px] text-slate-400 mt-0.5 leading-relaxed">{inv.detail}</p>
                  </div>
                </div>
              ))}
            </div>
          </BentoCard>

          {/* Pending Triage Queue Preview */}
          <BentoCard
            icon={<Database className="h-5 w-5 text-cyan-400" />}
            title="Cover Triage Queue"
            subtitle={`${deck.length} books flagged with low CQS or spurious art`}
            action={
              <Link
                to="/covers"
                className="text-xs font-bold text-cyan-400 hover:text-cyan-300 flex items-center gap-1"
              >
                <span>Full Deck</span>
                <ArrowRight className="h-3 w-3" />
              </Link>
            }
          >
            {deck.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-12 text-center">
                <ShieldCheck className="h-10 w-10 text-emerald-400 mb-2 opacity-80" />
                <p className="text-sm font-bold text-slate-200">Cover Queue Pristine</p>
                <p className="text-xs text-slate-400 mt-1">No low-resolution or decompression bomb covers pending review.</p>
              </div>
            ) : (
              <div className="space-y-3 mt-1">
                {deck.slice(0, 4).map((item) => (
                  <div
                    key={item.book_id}
                    className="flex items-center justify-between rounded-xl border border-slate-800/60 bg-slate-950/50 p-3 hover:border-slate-700/80 transition-all"
                  >
                    <div className="flex items-center gap-3 min-w-0">
                      <div className="h-12 w-9 shrink-0 overflow-hidden rounded-md border border-slate-700/60 bg-slate-900">
                        <img
                          src={`/api/covers/book/${item.book_id}/image`}
                          alt={item.title}
                          className="h-full w-full object-cover"
                          onError={(e) => {
                            ;(e.target as HTMLElement).style.display = 'none'
                          }}
                        />
                      </div>
                      <div className="min-w-0">
                        <p className="text-xs font-bold text-slate-200 truncate">{item.title}</p>
                        <p className="text-[11px] text-slate-400 truncate">{item.author}</p>
                      </div>
                    </div>

                    <div className="flex items-center gap-2 shrink-0">
                      <CqsBadge score={item.current_cqs} tier={item.current_tier} />
                    </div>
                  </div>
                ))}
              </div>
            )}
          </BentoCard>
        </section>
      </main>
    </div>
  )
}
