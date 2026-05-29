import { useEffect, useState } from 'react'
import { Check, X, ShieldAlert, Loader2, Sparkles, HelpCircle } from 'lucide-react'
import { fetchBooks, fetchEvidence, approvePatch, rejectPatch, applyPatches } from '../api/client'
import { useToast } from '../context/ToastContext'
import clsx from 'clsx'
import CoverImage from '../components/CoverImage'

export default function Review() {
  const [books, setBooks] = useState<any[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedBook, setSelectedBook] = useState<any | null>(null)
  const [evidence, setEvidence] = useState<any | null>(null)
  const [applying, setApplying] = useState(false)

  const { showToast } = useToast()

  useEffect(() => {
    loadBooks()
  }, [])

  const loadBooks = async () => {
    setLoading(true)
    try {
      const data = await fetchBooks()
      if (data && Array.isArray(data.data)) {
        // Only show books that actually need review
        const reviewable = data.data.filter((b: any) => 
          b.status === 'needs_review' || b.status === 'suggest_fix' || b.status === 'audited'
        )
        setBooks(reviewable)
      } else {
        setBooks([])
      }
    } catch (e) {
      showToast('Failed to load books', 'error')
      setBooks([])
    } finally {
      setLoading(false)
    }
  }

  const handleApplyAll = async () => {
    const approved = books.filter((b) => b.status === 'suggest_fix')
    if (approved.length === 0) {
      showToast('No approved patches to apply', 'error')
      return
    }
    const confirmed = window.confirm(
      `Apply ${approved.length} approved metadata patch(es) to your Calibre library? ` +
        'OPF backups will be created first. This cannot be undone except via Changes & Undo.'
    )
    if (!confirmed) return

    setApplying(true)
    try {
      const res = await applyPatches(true)
      showToast(res.data?.message || 'Approved patches applied successfully', 'success')
      loadBooks()
    } catch (e: any) {
      showToast(e.message || 'Failed to apply approved patches', 'error')
    } finally {
      setApplying(false)
    }
  }

  const handleSelectBook = async (book: any) => {
    setSelectedBook(book)
    setEvidence(null)
    try {
      const data = await fetchEvidence(book.book_key)
      setEvidence(data.data)
    } catch (e) {
      showToast('Failed to load book evidence details', 'error')
      console.error(e)
    }
  }

  const handleApprove = async (bookKey: string) => {
    try {
      await approvePatch(bookKey)
      setBooks(prev => prev.filter(b => b.book_key !== bookKey))
      setSelectedBook(null)
      showToast('Metadata patch approved and queued for application', 'success')
    } catch (e) {
      showToast('Failed to approve changes', 'error')
    }
  }

  const handleReject = async (bookKey: string) => {
    try {
      await rejectPatch(bookKey)
      setBooks(prev => prev.filter(b => b.book_key !== bookKey))
      setSelectedBook(null)
      showToast('Metadata patch rejected and removed from queue', 'info')
    } catch (e) {
      showToast('Failed to reject changes', 'error')
    }
  }

  // Render a visual comparison diff for metadata fields
  const renderDiff = () => {
    if (!evidence?.current || !evidence?.decision?.proposed_patch) return null

    const current = evidence.current
    const proposed = evidence.decision.proposed_patch
    
    // Combine all unique keys from both objects
    const allKeys = Array.from(new Set([
      ...Object.keys(current),
      ...Object.keys(proposed)
    ])).filter(k => k !== 'identifiers') // identifiers handled separately or skipped for simplicity

    return (
      <div className="space-y-4">
        {allKeys.map(key => {
          const oldVal = JSON.stringify(current[key])
          const newVal = JSON.stringify(proposed[key])
          const hasChanged = oldVal !== newVal

          if (!hasChanged) return null

          return (
            <div key={key} className="p-4 rounded-xl bg-slate-950/30 border border-slate-900 flex flex-col md:grid md:grid-cols-12 gap-4">
              <div className="md:col-span-3 flex items-center">
                <span className="font-bold font-mono text-xs uppercase tracking-wider text-slate-400 capitalize">{key.replace('_', ' ')}</span>
              </div>
              <div className="md:col-span-4 bg-rose-950/20 border border-rose-900/20 rounded-lg p-2.5 text-xs text-rose-300 font-mono break-all line-through">
                {current[key] !== undefined ? String(current[key]) : <span className="italic text-slate-600 font-sans">Not set</span>}
              </div>
              <div className="md:col-span-1 flex items-center justify-center text-slate-500 font-bold">
                →
              </div>
              <div className="md:col-span-4 bg-emerald-950/20 border border-emerald-900/20 rounded-lg p-2.5 text-xs text-emerald-300 font-mono break-all font-semibold">
                {proposed[key] !== undefined ? String(proposed[key]) : <span className="italic text-slate-600 font-sans">Delete</span>}
              </div>
            </div>
          )
        })}

        {/* Display Identifiers (like ISBN, Google Books ID) if changed */}
        {(() => {
          const oldId = current.identifiers || {}
          const newId = proposed.identifiers || {}
          const idKeys = Array.from(new Set([...Object.keys(oldId), ...Object.keys(newId)]))
          
          return idKeys.map(key => {
            const oldVal = oldId[key]
            const newVal = newId[key]
            if (oldVal === newVal) return null

            return (
              <div key={`id-${key}`} className="p-4 rounded-xl bg-slate-950/30 border border-slate-900 flex flex-col md:grid md:grid-cols-12 gap-4">
                <div className="md:col-span-3 flex items-center">
                  <span className="font-bold font-mono text-xs uppercase tracking-wider text-cyan-400">ID: {key.toUpperCase()}</span>
                </div>
                <div className="md:col-span-4 bg-rose-950/20 border border-rose-900/20 rounded-lg p-2.5 text-xs text-rose-300 font-mono break-all line-through">
                  {oldVal !== undefined ? String(oldVal) : <span className="italic text-slate-600 font-sans">Not set</span>}
                </div>
                <div className="md:col-span-1 flex items-center justify-center text-slate-500 font-bold">
                  →
                </div>
                <div className="md:col-span-4 bg-emerald-950/20 border border-emerald-900/20 rounded-lg p-2.5 text-xs text-emerald-300 font-mono break-all font-semibold">
                  {newVal !== undefined ? String(newVal) : <span className="italic text-slate-600 font-sans">Delete</span>}
                </div>
              </div>
            )
          })
        })()}
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-[calc(100vh-2rem)] page-transition">
      {/* Sidebar Queue */}
      <div className="w-80 border-r border-slate-800/40 bg-[#070b13]/80 flex flex-col backdrop-blur-md">
        <header className="p-4 border-b border-slate-800/40 space-y-3">
          <div className="flex items-center justify-between">
            <h1 className="text-md font-bold flex items-center gap-2 text-slate-200">
              <ShieldAlert className="w-5 h-5 text-purple-500 drop-shadow-[0_0_6px_rgba(139,92,246,0.3)]" />
              Review Queue
            </h1>
            <p className="text-[10px] text-slate-500 uppercase tracking-widest font-mono">{books.length} items</p>
          </div>
          
          <button
            onClick={handleApplyAll}
            disabled={applying || books.length === 0}
            className="w-full flex items-center justify-center gap-2 py-2 bg-gradient-to-r from-emerald-600 to-teal-600 hover:from-emerald-500 hover:to-teal-500 disabled:opacity-40 disabled:cursor-not-allowed text-white text-xs font-bold rounded-xl transition-all duration-300 shadow-[0_0_10px_rgba(16,185,129,0.15)] cursor-pointer"
          >
            {applying ? (
              <>
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
                Applying fixes...
              </>
            ) : (
              <>
                <Sparkles className="w-3.5 h-3.5" />
                Apply Approved Fixes
              </>
            )}
          </button>
        </header>
        
        <div className="flex-1 overflow-y-auto divide-y divide-slate-800/30">
          {loading ? (
            <div className="p-12 flex justify-center">
              <Loader2 className="animate-spin text-purple-500 w-6 h-6" />
            </div>
          ) : Array.isArray(books) && books.length > 0 ? (
            books.map((book) => (
              <button
                key={book.book_key}
                onClick={() => handleSelectBook(book)}
                className={clsx(
                  "w-full text-left p-4 hover:bg-slate-900/30 transition-all duration-300 relative border-l-2",
                  selectedBook?.book_key === book.book_key 
                    ? "bg-purple-950/15 border-l-purple-500 text-purple-200" 
                    : "border-l-transparent text-slate-300 hover:text-slate-100"
                )}
              >
                <p className="font-semibold text-sm line-clamp-1">{book.current_metadata.title}</p>
                <p className="text-xs text-slate-500 mt-1 line-clamp-1">by {book.current_metadata.authors.join(', ')}</p>
              </button>
            ))
          ) : (
            <div className="p-8 text-center text-xs text-slate-500 italic">
              All caught up! Queue is empty.
            </div>
          )}
        </div>
      </div>

      {/* Main Review Panel */}
      <div className="flex-1 bg-[#090d16]/30 overflow-y-auto relative">
        {selectedBook ? (
          <div className="p-8 max-w-5xl mx-auto space-y-8 animate-in fade-in duration-300">
            {/* Header / Actions */}
            <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 border-b border-slate-800/40 pb-6">
              <div className="flex gap-4">
                <CoverImage bookKey={selectedBook.book_key} className="w-16 h-20 shrink-0" />
                <div className="flex flex-col justify-center">
                  <h2 className="text-2xl font-extrabold text-slate-100 tracking-tight">{selectedBook.current_metadata.title}</h2>
                  <p className="text-slate-400 text-sm mt-1">by {selectedBook.current_metadata.authors.join(', ')}</p>
                </div>
              </div>
              
              <div className="flex items-center gap-3 shrink-0">
                <button 
                  onClick={() => handleReject(selectedBook.book_key)}
                  className="flex items-center gap-2 px-4.5 py-2.5 bg-slate-900 hover:bg-slate-800/80 border border-slate-800/80 text-rose-400 rounded-xl transition-all duration-200 font-semibold text-sm cursor-pointer"
                >
                  <X className="w-4 h-4" />
                  Reject
                </button>
                <button 
                  onClick={() => handleApprove(selectedBook.book_key)}
                  className="flex items-center gap-2 px-5 py-2.5 bg-gradient-to-r from-purple-600 to-indigo-600 hover:from-purple-500 hover:to-indigo-500 text-white rounded-xl transition-all duration-200 shadow-[0_0_12px_rgba(139,92,246,0.2)] font-semibold text-sm cursor-pointer"
                >
                  <Check className="w-4 h-4" />
                  Approve Changes
                </button>
              </div>
            </div>

            {evidence ? (
              <div className="space-y-8">
                {/* Decision Summary Diff */}
                {evidence.decision && (
                  <div className="glass-card p-6 rounded-2xl space-y-4">
                    <div className="flex items-center gap-2 text-slate-200 font-bold border-b border-slate-800/40 pb-3">
                      <Sparkles className="w-5 h-5 text-purple-400" />
                      Proposed Changes Summary
                    </div>
                    {renderDiff()}
                  </div>
                )}

                {/* Evidence Ladder */}
                <div className="glass-card p-6 rounded-2xl space-y-4">
                  <h3 className="text-md font-bold text-slate-200 flex items-center gap-2 border-b border-slate-800/40 pb-3">
                    <HelpCircle className="w-4 h-4 text-cyan-400" />
                    Confidence & Evidence Ladder
                  </h3>
                  <div className="grid grid-cols-1 gap-3.5">
                    {evidence.snippets?.map((s: any, i: number) => (
                      <div key={i} className="p-4 bg-slate-950/40 border border-slate-900/60 rounded-xl">
                        <div className="flex items-center justify-between mb-1.5">
                          <span className="text-[9px] px-2 py-0.5 bg-purple-500/10 text-purple-400 border border-purple-500/20 rounded font-bold uppercase tracking-wider font-mono">
                            {s.source}
                          </span>
                          {s.confidence && (
                            <span className="text-[10px] text-slate-500 font-mono">Confidence: {s.confidence}%</span>
                          )}
                        </div>
                        <p className="text-xs text-slate-300 italic">"{s.text}"</p>
                      </div>
                    ))}
                  </div>
                </div>
              </div>
            ) : (
              <div className="p-24 flex flex-col items-center justify-center text-slate-500">
                <Loader2 className="w-8 h-8 animate-spin text-purple-500 mb-4" />
                <p className="text-sm font-medium">Extracting evidence package...</p>
              </div>
            )}
          </div>
        ) : (
          <div className="h-full flex flex-col items-center justify-center text-slate-500 p-8">
            <ShieldAlert className="w-12 h-12 text-slate-700 mb-3 animate-pulse-soft" />
            <p className="text-sm italic">Select a book from the queue to review changes</p>
          </div>
        )}
      </div>
    </div>
  )
}
