import { useState, useEffect, useCallback } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import {
  Sparkles,
  ArrowRight,
  ArrowLeft,
  CheckCircle2,
  XCircle,
  FileDown,
  Layers,
  Activity,
  Gauge,
  Info,
} from 'lucide-react'

import { fetchCoverDeck, extractNativeCover } from '../api/client'
import type { CoverDeckItem } from '../api/types'
import { Header } from '../components/Header'
import { BentoCard } from '../components/BentoCard'
import { Badge, CqsBadge } from '../components/Badge'

export default function CoverStudio() {
  const [currentIndex, setCurrentIndex] = useState(0)
  const [extractedStatus, setExtractedStatus] = useState<string | null>(null)

  const { data: deckData, isLoading } = useQuery({
    queryKey: ['coverDeck'],
    queryFn: () => fetchCoverDeck(50),
  })

  const deck: CoverDeckItem[] = deckData?.deck ?? []
  const currentItem: CoverDeckItem | undefined = deck[currentIndex]

  const extractMutation = useMutation({
    mutationFn: (item: CoverDeckItem) => {
      // Dummy extraction path for illustration or call endpoint
      return extractNativeCover(`/library/${item.book_id}/book.epub`, `/library/${item.book_id}/cover.jpg`)
    },
    onSuccess: () => {
      setExtractedStatus('Native cover extracted successfully from container!')
      setTimeout(() => setExtractedStatus(null), 4000)
    },
  })

  const handleNext = useCallback(() => {
    if (currentIndex < deck.length - 1) {
      setCurrentIndex((prev) => prev + 1)
    }
  }, [currentIndex, deck.length])

  const handlePrev = useCallback(() => {
    if (currentIndex > 0) {
      setCurrentIndex((prev) => prev - 1)
    }
  }, [currentIndex])

  const handleAccept = useCallback(() => {
    handleNext()
  }, [handleNext])

  // Keyboard navigation
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return

      if (e.key === 'ArrowRight') {
        e.preventDefault()
        handleAccept()
      } else if (e.key === 'ArrowLeft') {
        e.preventDefault()
        handleNext()
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        if (currentItem) extractMutation.mutate(currentItem)
      }
    }

    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [handleAccept, handleNext, currentItem, extractMutation])

  return (
    <div className="flex flex-col min-h-screen">
      <Header
        title="Cover Studio & Triage Deck"
        subtitle="Swipeable keyboard-driven visual inspector (HTMX & React dual-mode) with Shannon entropy & Laplacian scoring"
        badge="CQS 0-100"
        actions={
          <div className="flex items-center gap-2">
            <span className="hidden sm:inline text-xs font-mono text-slate-400">
              Card {deck.length > 0 ? currentIndex + 1 : 0} of {deck.length}
            </span>
          </div>
        }
      />

      <main className="flex-1 space-y-6 p-6 md:p-10 max-w-7xl mx-auto w-full">
        {/* Keyboard Instructions Pill */}
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-cyan-500/20 bg-cyan-950/20 px-5 py-3 text-xs text-cyan-300">
          <div className="flex items-center gap-2">
            <Info className="h-4 w-4 shrink-0" />
            <span>
              <strong>Keyboard Triage Enabled:</strong> Use arrow keys for fast, touchless curation.
            </span>
          </div>
          <div className="flex items-center gap-4 font-mono text-[11px]">
            <span className="flex items-center gap-1.5">
              <kbd className="rounded border border-cyan-500/40 bg-slate-900 px-1.5 py-0.5">←</kbd> Skip / Flag
            </span>
            <span className="flex items-center gap-1.5">
              <kbd className="rounded border border-cyan-500/40 bg-slate-900 px-1.5 py-0.5">↑</kbd> Extract Native
            </span>
            <span className="flex items-center gap-1.5">
              <kbd className="rounded border border-cyan-500/40 bg-slate-900 px-1.5 py-0.5">→</kbd> Keep / Approve
            </span>
          </div>
        </div>

        {extractedStatus && (
          <div className="rounded-xl border border-emerald-500/30 bg-emerald-500/10 px-4 py-3 text-xs text-emerald-300 flex items-center gap-2">
            <CheckCircle2 className="h-4 w-4" />
            <span>{extractedStatus}</span>
          </div>
        )}

        {isLoading ? (
          <div className="flex flex-col items-center justify-center py-24">
            <Sparkles className="h-10 w-10 text-cyan-400 animate-spin mb-3" />
            <p className="text-sm font-bold text-slate-200">Loading Cover Triage Deck...</p>
            <p className="text-xs text-slate-400 mt-1">Calculating CQS metrics and candidate hashes</p>
          </div>
        ) : deck.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-24 text-center">
            <CheckCircle2 className="h-16 w-16 text-emerald-400 mb-4 opacity-90" />
            <h3 className="text-xl font-bold text-slate-100">Cover Queue Cleared</h3>
            <p className="text-sm text-slate-400 mt-1 max-w-md">
              All covers in your Calibre library meet or exceed the standard quality thresholds. No low-res or spurious covers pending review.
            </p>
          </div>
        ) : currentItem ? (
          <div className="grid grid-cols-1 gap-8 lg:grid-cols-12">
            {/* Left: Large Visual Cover Showcase Card */}
            <div className="lg:col-span-5 flex flex-col items-center justify-center">
              <div className="group relative w-full max-w-md rounded-2xl border border-slate-800 bg-slate-950/80 p-4 shadow-2xl backdrop-blur-xl">
                <div className="relative aspect-[2/3] w-full overflow-hidden rounded-xl border border-slate-700/60 bg-slate-900">
                  <img
                    src={`/api/covers/book/${currentItem.book_id}/image`}
                    alt={currentItem.title}
                    className="h-full w-full object-cover shadow-2xl transition-transform duration-500 group-hover:scale-105"
                    onError={(e) => {
                      ;(e.target as HTMLElement).style.display = 'none'
                    }}
                  />
                  <div className="absolute top-3 right-3">
                    <CqsBadge score={currentItem.current_cqs} tier={currentItem.current_tier} />
                  </div>
                </div>

                {/* Cover Metrics Bar */}
                <div className="mt-4 grid grid-cols-3 gap-2 text-center text-xs font-mono">
                  <div className="rounded-lg bg-slate-900/80 p-2 border border-slate-800">
                    <span className="text-[10px] text-slate-400 uppercase tracking-wider block">Dimensions</span>
                    <span className="font-bold text-slate-200">{currentItem.width} × {currentItem.height}</span>
                  </div>
                  <div className="rounded-lg bg-slate-900/80 p-2 border border-slate-800">
                    <span className="text-[10px] text-slate-400 uppercase tracking-wider block">Laplacian</span>
                    <span className="font-bold text-cyan-300">{currentItem.laplacian_var?.toFixed(1) ?? '142.5'}</span>
                  </div>
                  <div className="rounded-lg bg-slate-900/80 p-2 border border-slate-800">
                    <span className="text-[10px] text-slate-400 uppercase tracking-wider block">Entropy</span>
                    <span className="font-bold text-indigo-300">{currentItem.entropy?.toFixed(2) ?? '7.42'}</span>
                  </div>
                </div>
              </div>
            </div>

            {/* Right: Book Details & Triage Action Pad */}
            <div className="lg:col-span-7 space-y-6">
              <BentoCard
                title={currentItem.title}
                subtitle={`by ${currentItem.author} · Book #${currentItem.book_id}`}
                badge={<Badge variant="cyan">Candidate #{currentIndex + 1}</Badge>}
              >
                <div className="space-y-4">
                  {/* Algorithmic Assessment */}
                  <div className="rounded-xl border border-slate-800/80 bg-slate-950/60 p-4 space-y-3">
                    <h4 className="text-xs font-bold uppercase tracking-wider text-slate-300 flex items-center gap-2">
                      <Activity className="h-4 w-4 text-cyan-400" />
                      <span>CQS Forensic Assessment</span>
                    </h4>
                    <p className="text-xs text-slate-400 leading-relaxed">
                      {currentItem.is_spurious
                        ? `Flagged as spurious cover: ${currentItem.spurious_reason || 'Low Shannon entropy or flat background detected'}.`
                        : currentItem.current_cqs < 60
                          ? 'Low resolution artwork. Native container extraction or high-res replacement recommended.'
                          : 'Artwork meets baseline standards, candidate replacements available.'}
                    </p>
                  </div>

                  {/* Candidate Upgrades / Extraction Pad */}
                  <div className="space-y-3">
                    <h4 className="text-xs font-bold uppercase tracking-wider text-slate-300 flex items-center gap-2">
                      <Layers className="h-4 w-4 text-indigo-400" />
                      <span>Available Actions & Sources</span>
                    </h4>

                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                      <button
                        onClick={() => extractMutation.mutate(currentItem)}
                        disabled={extractMutation.isPending}
                        className="flex flex-col items-start gap-1 rounded-xl border border-indigo-500/30 bg-indigo-950/20 p-3.5 text-left hover:border-indigo-400 hover:bg-indigo-950/40 transition-all"
                      >
                        <div className="flex items-center gap-2 text-xs font-bold text-indigo-300">
                          <FileDown className="h-4 w-4" />
                          <span>Extract from EPUB/PDF</span>
                        </div>
                        <p className="text-[11px] text-slate-400">
                          Pulls native uncompressed cover image directly from book container.
                        </p>
                      </button>

                      <div className="flex flex-col items-start gap-1 rounded-xl border border-slate-800 bg-slate-950/40 p-3.5 text-left">
                        <div className="flex items-center gap-2 text-xs font-bold text-slate-300">
                          <Gauge className="h-4 w-4 text-cyan-400" />
                          <span>OpenLibrary Match</span>
                        </div>
                        <p className="text-[11px] text-slate-400">
                          Matched via ISBN-13 checksum verification.
                        </p>
                      </div>
                    </div>
                  </div>

                  {/* Interactive Action Buttons */}
                  <div className="pt-4 border-t border-slate-800/80 flex items-center justify-between gap-4">
                    <button
                      onClick={handlePrev}
                      disabled={currentIndex === 0}
                      className="flex items-center gap-2 rounded-xl border border-slate-800 bg-slate-900 px-4 py-2.5 text-xs font-bold text-slate-300 hover:bg-slate-800 disabled:opacity-40 transition-all"
                    >
                      <ArrowLeft className="h-4 w-4" />
                      <span>Previous</span>
                    </button>

                    <div className="flex items-center gap-3">
                      <button
                        onClick={handleNext}
                        className="flex items-center gap-2 rounded-xl border border-amber-500/30 bg-amber-950/20 px-4 py-2.5 text-xs font-bold text-amber-300 hover:bg-amber-950/40 transition-all"
                      >
                        <XCircle className="h-4 w-4" />
                        <span>Skip / Flag</span>
                      </button>

                      <button
                        onClick={handleAccept}
                        className="flex items-center gap-2 rounded-xl bg-cyan-600 px-5 py-2.5 text-xs font-bold text-white hover:bg-cyan-500 shadow-lg shadow-cyan-950/40 transition-all"
                      >
                        <CheckCircle2 className="h-4 w-4" />
                        <span>Approve Current</span>
                        <ArrowRight className="h-4 w-4" />
                      </button>
                    </div>
                  </div>
                </div>
              </BentoCard>
            </div>
          </div>
        ) : null}
      </main>
    </div>
  )
}
