import { useQuery } from '@tanstack/react-query'
import { fetchDuplicates, fetchBooks } from '../api/client'
import { Link } from 'react-router-dom'
import { Copy, AlertTriangle, FileSearch, Loader2 } from 'lucide-react'
import clsx from 'clsx'
import CoverImage from '../components/CoverImage'

interface DuplicateItem {
  type: 'isbn_match' | 'title_author_match' | 'semantic_similarity'
  books: string[]
  value: string
}

export default function Duplicates() {
  const { data: dupsData, isLoading: dupsLoading, isError: dupsError, error: dupsErr } = useQuery({
    queryKey: ['duplicates'],
    queryFn: fetchDuplicates,
  })

  const { data: booksData, isLoading: booksLoading } = useQuery({
    queryKey: ['books'],
    queryFn: fetchBooks,
  })

  const isLoading = dupsLoading || booksLoading
  const duplicates: DuplicateItem[] = dupsData?.data || []
  const booksList = booksData?.data || []

  // Create a mapping of book_key -> book object
  const booksMap = booksList.reduce((acc: any, b: any) => {
    acc[b.book_key] = b
    return acc
  }, {})

  if (isLoading) {
    return (
      <div className="p-24 flex flex-col items-center justify-center text-slate-500">
        <Loader2 className="animate-spin text-purple-500 w-8 h-8 mb-4" />
        <p className="text-sm font-medium">Analyzing database for duplicates...</p>
      </div>
    )
  }

  if (dupsError) {
    return (
      <div className="p-8 max-w-6xl mx-auto space-y-4">
        <div className="p-4 bg-rose-500/10 border border-rose-500/20 text-rose-400 rounded-xl">
          <p className="font-semibold">Failed to fetch duplicates</p>
          <p className="text-xs mt-1">{(dupsErr as Error).message}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="p-8 max-w-6xl mx-auto space-y-8 page-transition">
      <header className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
        <div>
          <h1 className="text-3xl font-extrabold tracking-tight bg-clip-text text-transparent bg-gradient-to-r from-slate-100 to-slate-400">
            Duplicate Detection
          </h1>
          <p className="text-slate-400 text-sm mt-1">
            Review matching ISBN codes, title/author combinations, and semantic text similarities in your Calibre library.
          </p>
        </div>
        <div className="flex items-center gap-2 px-4 py-2 rounded-xl bg-slate-900/40 border border-slate-800/40 backdrop-blur-md">
          <span className="text-xs text-slate-500 font-mono">DENSITY:</span>
          <span className="text-xs font-semibold text-purple-400 font-mono">
            {duplicates.length} {duplicates.length === 1 ? 'Pair' : 'Pairs'}
          </span>
        </div>
      </header>

      {duplicates.length === 0 ? (
        <div className="glass-card p-12 text-center rounded-2xl max-w-2xl mx-auto space-y-4">
          <div className="mx-auto w-12 h-12 rounded-full bg-emerald-500/10 border border-emerald-500/20 flex items-center justify-center text-emerald-400">
            <Copy className="w-6 h-6" />
          </div>
          <div className="space-y-1">
            <h3 className="text-lg font-bold text-slate-200">No Duplicates Found</h3>
            <p className="text-xs text-slate-400">
              Your database has clean metadata records. No matching ISBNs, title/author overlaps, or semantic similarities were identified.
            </p>
          </div>
        </div>
      ) : (
        <div className="space-y-6">
          {duplicates.map((dup, idx) => {
            const bookA = booksMap[dup.books[0]]
            const bookB = booksMap[dup.books[1]]

            // Fallback object if book data is not found in local cache
            const getBookView = (key: string, record: any) => {
              if (!record) {
                return (
                  <div className="p-4 bg-slate-950/30 border border-slate-900 rounded-xl space-y-1">
                    <p className="text-xs font-mono text-slate-500">KEY: {key}</p>
                    <p className="text-sm font-semibold text-slate-400">Record not found in current run cache</p>
                  </div>
                )
              }

              const formatList = record.files?.map((f: any) => f.format.toUpperCase()).join(', ') || 'N/A'
              const primaryPath = record.files?.[0]?.path || ''

              return (
                <div className="p-5 bg-slate-950/30 border border-slate-900 rounded-xl space-y-3 flex flex-col justify-between h-full">
                  <div className="flex gap-4">
                    <CoverImage bookKey={record.book_key} className="w-16 h-20 shrink-0" />
                    <div className="space-y-2 flex-1 min-w-0">
                      <div className="flex justify-between items-start gap-2">
                        <span className="text-[10px] font-mono text-slate-500 uppercase font-semibold">
                          {record.book_key}
                        </span>
                        {formatList && (
                          <span className="px-2 py-0.5 rounded bg-slate-900 text-[10px] text-slate-400 font-mono border border-slate-800">
                            {formatList}
                          </span>
                        )}
                      </div>
                      <div>
                        <h4 className="text-sm font-bold text-slate-200 leading-tight truncate">
                          {record.current_metadata?.title || 'Unknown Title'}
                        </h4>
                        <p className="text-xs text-slate-400 mt-1 truncate">
                          By {record.current_metadata?.authors?.join(', ') || 'Unknown'}
                        </p>
                      </div>

                      {record.current_metadata?.identifiers?.isbn && (
                        <p className="text-[10px] font-mono text-slate-500">
                          ISBN: <span className="text-slate-400">{record.current_metadata.identifiers.isbn}</span>
                        </p>
                      )}
                    </div>
                  </div>

                  {primaryPath && (
                    <div className="pt-2 border-t border-slate-900/60 flex items-center justify-between gap-4">
                      <span className="text-[10px] text-slate-500 font-mono truncate max-w-[200px]" title={primaryPath}>
                        {primaryPath.split('/').pop()}
                      </span>
                      <Link
                        to={`/inspect?path=${encodeURIComponent(primaryPath)}`}
                        className="inline-flex items-center gap-1 text-[10px] font-bold text-purple-400 hover:text-purple-300 transition-colors uppercase font-mono"
                      >
                        <FileSearch className="w-3.5 h-3.5" />
                        Inspect
                      </Link>
                    </div>
                  )}
                </div>
              )
            }

            return (
              <div key={idx} className="glass-card p-6 rounded-2xl space-y-4">
                {/* Meta details */}
                <div className="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-2 border-b border-slate-800/40 pb-3">
                  <div className="flex items-center gap-2">
                    <AlertTriangle className={clsx(
                      "w-4 h-4",
                      dup.type === 'isbn_match' ? "text-cyan-400" :
                      dup.type === 'title_author_match' ? "text-amber-400" : "text-purple-400"
                    )} />
                    <span className="text-xs font-semibold text-slate-200 capitalize">
                      {dup.type.replace(/_/g, ' ')}
                    </span>
                  </div>
                  <div className="px-3 py-1 bg-slate-950/60 border border-slate-900/80 rounded-lg text-xs font-mono text-slate-400 max-w-full truncate">
                    {dup.type === 'isbn_match' && `ISBN: ${dup.value}`}
                    {dup.type === 'title_author_match' && `Title|Author combo: ${dup.value}`}
                    {dup.type === 'semantic_similarity' && `${dup.value}`}
                  </div>
                </div>

                {/* Side-by-side */}
                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                  {getBookView(dup.books[0], bookA)}
                  {getBookView(dup.books[1], bookB)}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
