import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Copy,
  Layers,
  Search,
  CheckCircle2,
  AlertCircle,
} from 'lucide-react'
import clsx from 'clsx'

import { fetchSeriesGaps, fetchDuplicateClusters } from '../api/client'
import type { SeriesGap, DuplicateCluster } from '../api/types'
import { Header } from '../components/Header'
import { BentoCard } from '../components/BentoCard'
import { Badge, FormatBadge } from '../components/Badge'

type CurationTab = 'gaps' | 'duplicates'

export default function Curation() {
  const [activeTab, setActiveTab] = useState<CurationTab>('gaps')
  const [searchQuery, setSearchQuery] = useState('')

  const { data: gaps = [], isLoading: gapsLoading } = useQuery<SeriesGap[]>({
    queryKey: ['seriesGaps'],
    queryFn: fetchSeriesGaps,
  })

  const { data: duplicates = [], isLoading: dupLoading } = useQuery<DuplicateCluster[]>({
    queryKey: ['duplicateClusters'],
    queryFn: fetchDuplicateClusters,
  })

  const filteredGaps = gaps.filter(
    (g) =>
      g.series_name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      g.authors.toLowerCase().includes(searchQuery.toLowerCase()),
  )

  const filteredDuplicates = duplicates.filter(
    (d) =>
      d.title.toLowerCase().includes(searchQuery.toLowerCase()) ||
      d.author.toLowerCase().includes(searchQuery.toLowerCase()),
  )

  return (
    <div className="flex flex-col min-h-screen">
      <Header
        title="Curation Guardian"
        subtitle="Algorithmic series sequence completion and FRBR multi-format duplicate consolidation"
        badge="FRBR & Sagas"
        actions={
          <div className="relative">
            <Search className="absolute left-3 top-2.5 h-3.5 w-3.5 text-slate-400" />
            <input
              type="text"
              placeholder="Filter series or titles..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="w-48 sm:w-64 rounded-xl border border-slate-800 bg-slate-900/80 pl-9 pr-3 py-1.5 text-xs text-slate-200 placeholder-slate-500 focus:border-cyan-500/60 focus:outline-none transition-all"
            />
          </div>
        }
      />

      <main className="flex-1 space-y-6 p-6 md:p-10 max-w-7xl mx-auto w-full">
        {/* Module Switcher Tabs */}
        <div className="flex border-b border-slate-800/80">
          <button
            onClick={() => setActiveTab('gaps')}
            className={clsx(
              'flex items-center gap-2.5 px-5 py-3.5 text-xs font-bold transition-all border-b-2 -mb-px',
              activeTab === 'gaps'
                ? 'border-amber-400 text-amber-300 bg-amber-500/5'
                : 'border-transparent text-slate-400 hover:text-slate-200',
            )}
          >
            <Layers className="h-4 w-4" />
            <span>Series Gap Hunter</span>
            <span className="rounded-full bg-amber-500/20 px-2 py-0.5 text-[10px] font-mono text-amber-300">
              {gaps.length}
            </span>
          </button>

          <button
            onClick={() => setActiveTab('duplicates')}
            className={clsx(
              'flex items-center gap-2.5 px-5 py-3.5 text-xs font-bold transition-all border-b-2 -mb-px',
              activeTab === 'duplicates'
                ? 'border-indigo-400 text-indigo-300 bg-indigo-500/5'
                : 'border-transparent text-slate-400 hover:text-slate-200',
            )}
          >
            <Copy className="h-4 w-4" />
            <span>Multi-Format Duplicates</span>
            <span className="rounded-full bg-indigo-500/20 px-2 py-0.5 text-[10px] font-mono text-indigo-300">
              {duplicates.length}
            </span>
          </button>
        </div>

        {/* Tab 1: Series Gap Hunter */}
        {activeTab === 'gaps' && (
          <div className="space-y-4">
            <div className="flex items-center justify-between">
              <div>
                <h3 className="text-sm font-bold text-slate-200">Missing Intermediate & Leading Volumes</h3>
                <p className="text-xs text-slate-400">
                  Detects gaps in book numbering (e.g., owning Volume 1, 2, and 4; flagging missing Volume 3).
                </p>
              </div>
              <Badge variant="amber">Runaway guard: MAX_GAP_SPAN = 200</Badge>
            </div>

            {gapsLoading ? (
              <div className="flex flex-col items-center justify-center py-20">
                <Layers className="h-8 w-8 text-amber-400 animate-pulse mb-2" />
                <p className="text-xs font-bold text-slate-300">Hunting Series Gaps across library...</p>
              </div>
            ) : filteredGaps.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-20 text-center rounded-2xl border border-slate-800 bg-slate-950/60 p-8">
                <CheckCircle2 className="h-12 w-12 text-emerald-400 mb-3 opacity-90" />
                <h4 className="text-base font-bold text-slate-100">All Tracked Sagas Complete</h4>
                <p className="text-xs text-slate-400 mt-1 max-w-md">
                  No missing intermediate or leading numbers found in your series catalog.
                </p>
              </div>
            ) : (
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                {filteredGaps.map((gap) => (
                  <BentoCard
                    key={gap.series_id}
                    title={gap.series_name}
                    subtitle={`by ${gap.authors} · ${gap.total_owned} volumes owned`}
                    badge={<Badge variant="amber">Missing {gap.missing_indices.length} vol</Badge>}
                  >
                    <div className="space-y-4 mt-2">
                      {/* Missing Volumes Callout */}
                      <div className="flex flex-wrap items-center gap-1.5 p-3 rounded-xl border border-amber-500/30 bg-amber-950/20">
                        <span className="text-xs font-bold text-amber-300 mr-2 flex items-center gap-1">
                          <AlertCircle className="h-3.5 w-3.5" />
                          Missing Volumes:
                        </span>
                        {gap.missing_indices.map((idx) => (
                          <span
                            key={idx}
                            className="rounded-md border border-amber-500/50 bg-amber-500/20 px-2 py-0.5 text-xs font-mono font-bold text-amber-200"
                          >
                            Vol. {idx}
                          </span>
                        ))}
                      </div>

                      {/* Owned Volumes Shelf List */}
                      <div className="space-y-1.5">
                        <span className="text-[10px] font-bold uppercase tracking-wider text-slate-400">
                          Owned Shelf ({gap.books.length} records)
                        </span>
                        <div className="max-h-36 overflow-y-auto space-y-1 pr-1">
                          {gap.books.map((b) => (
                            <div
                              key={b.id}
                              className="flex items-center justify-between rounded-lg bg-slate-900/60 px-3 py-1.5 text-xs border border-slate-800/40"
                            >
                              <span className="font-semibold text-slate-200 truncate max-w-xs">{b.title}</span>
                              <span className="font-mono text-cyan-400 font-bold text-[11px] shrink-0">
                                Vol. {b.series_index}
                              </span>
                            </div>
                          ))}
                        </div>
                      </div>
                    </div>
                  </BentoCard>
                ))}
              </div>
            )}
          </div>
        )}

        {/* Tab 2: Duplicate Work Consolidator */}
        {activeTab === 'duplicates' && (
          <div className="space-y-4">
            <div className="flex items-center justify-between">
              <div>
                <h3 className="text-sm font-bold text-slate-200">FRBR Multi-Format Clusters & ISBN Collisions</h3>
                <p className="text-xs text-slate-400">
                  Identifies separate Calibre entries representing identical works with differing formats (e.g. EPUB + PDF).
                </p>
              </div>
              <Badge variant="purple">NetworkX Graph Clusters</Badge>
            </div>

            {dupLoading ? (
              <div className="flex flex-col items-center justify-center py-20">
                <Copy className="h-8 w-8 text-indigo-400 animate-pulse mb-2" />
                <p className="text-xs font-bold text-slate-300">Analyzing duplicate clusters...</p>
              </div>
            ) : filteredDuplicates.length === 0 ? (
              <div className="flex flex-col items-center justify-center py-20 text-center rounded-2xl border border-slate-800 bg-slate-950/60 p-8">
                <CheckCircle2 className="h-12 w-12 text-emerald-400 mb-3 opacity-90" />
                <h4 className="text-base font-bold text-slate-100">Zero Unconsolidated Duplicates</h4>
                <p className="text-xs text-slate-400 mt-1 max-w-md">
                  No multi-format split records or colliding ISBN records detected.
                </p>
              </div>
            ) : (
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                {filteredDuplicates.map((cluster, idx) => (
                  <BentoCard
                    key={idx}
                    title={cluster.title}
                    subtitle={`by ${cluster.author}`}
                    badge={
                      <Badge variant={cluster.cluster_type === 'isbn_collision' ? 'amber' : 'purple'}>
                        {cluster.cluster_type === 'isbn_collision' ? 'ISBN Collision' : 'Multi-Format Split'}
                      </Badge>
                    }
                  >
                    <div className="space-y-3 mt-2">
                      <div className="flex items-center justify-between text-xs text-slate-400 border-b border-slate-800/60 pb-2">
                        <span>Recommendation</span>
                        <span className="font-bold text-indigo-300">{cluster.recommendation}</span>
                      </div>

                      {/* Books involved */}
                      <div className="space-y-2">
                        <span className="text-[10px] font-bold uppercase tracking-wider text-slate-400">
                          Linked Calibre Book Records
                        </span>
                        <div className="space-y-1.5">
                          {[cluster.primary_book_id, ...cluster.duplicate_book_ids].map((bookId) => {
                            const formats = cluster.formats_by_book[bookId] || ['EPUB']
                            const isPrimary = bookId === cluster.primary_book_id

                            return (
                              <div
                                key={bookId}
                                className="flex items-center justify-between rounded-lg bg-slate-900/60 px-3 py-2 text-xs border border-slate-800/60"
                              >
                                <div className="flex items-center gap-2">
                                  <span className="font-mono text-slate-400">#{bookId}</span>
                                  {isPrimary && (
                                    <span className="rounded bg-emerald-500/20 px-1.5 py-0.5 text-[9px] font-bold text-emerald-300 uppercase">
                                      Primary
                                    </span>
                                  )}
                                </div>
                                <div className="flex items-center gap-1.5">
                                  {formats.map((fmt) => (
                                    <FormatBadge key={fmt} format={fmt} />
                                  ))}
                                </div>
                              </div>
                            )
                          })}
                        </div>
                      </div>
                    </div>
                  </BentoCard>
                ))}
              </div>
            )}
          </div>
        )}
      </main>
    </div>
  )
}
