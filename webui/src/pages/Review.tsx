import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import {
  AlertTriangle,
  CheckCircle2,
  FileCheck2,
  FileKey2,
  Fingerprint,
  Hash,
  Loader2,
  LockKeyhole,
  ShieldCheck,
} from 'lucide-react'
import clsx from 'clsx'

import {
  fetchReviewV2,
  fetchReviewV2Detail,
  type EvidencePackageV2,
  type IdentityTier,
} from '../api/client'

const TIER_STYLE: Record<IdentityTier, string> = {
  A: 'border-emerald-600/50 bg-emerald-950/35 text-emerald-300',
  B: 'border-amber-600/50 bg-amber-950/35 text-amber-300',
  C: 'border-rose-600/50 bg-rose-950/35 text-rose-300',
}

function titleOf(metadata: Record<string, unknown>): string {
  return typeof metadata.title === 'string' && metadata.title.trim() ? metadata.title : 'Untitled record'
}

function authorsOf(metadata: Record<string, unknown>): string {
  if (Array.isArray(metadata.authors)) return metadata.authors.map(String).filter(Boolean).join(', ') || 'Unknown author'
  return typeof metadata.authors === 'string' && metadata.authors.trim() ? metadata.authors : 'Unknown author'
}

function valueOf(value: unknown): string {
  if (value === null || value === undefined || value === '') return '∅'
  if (Array.isArray(value)) return value.map(String).join(', ')
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

function currentValue(pkg: EvidencePackageV2, field: string): unknown {
  if (field === 'edition_statement') return pkg.snapshot.current_metadata['#edition'] ?? pkg.snapshot.current_metadata.edition_statement
  return pkg.snapshot.current_metadata[field]
}

export default function Review() {
  const { evidenceId } = useParams<{ evidenceId: string }>()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const runId = searchParams.get('run_id') ?? undefined
  const [tier, setTier] = useState<'all' | IdentityTier>('all')
  const [offset, setOffset] = useState(0)

  useEffect(() => setOffset(0), [runId, tier])

  const listQuery = useQuery({
    queryKey: ['reviewV2', runId, tier, offset],
    queryFn: () => fetchReviewV2({ runId, tier: tier === 'all' ? undefined : tier, limit: 50, offset }),
  })
  const detailQuery = useQuery({
    queryKey: ['reviewV2Detail', evidenceId],
    queryFn: () => fetchReviewV2Detail(evidenceId!),
    enabled: Boolean(evidenceId),
  })

  const items = listQuery.data?.data ?? []
  const total = listQuery.data?.meta.total ?? 0
  const pkg = detailQuery.data?.package
  const patchEntries = useMemo(() => Object.entries(pkg?.identity.auto_patch ?? {}), [pkg])

  return (
    <div className="min-h-full lg:flex">
      <aside className="border-b border-slate-800/60 bg-[#070b13]/80 lg:w-[23rem] lg:shrink-0 lg:border-b-0 lg:border-r">
        <header className="space-y-4 border-b border-slate-800/60 p-5">
          <div>
            <p className="text-[10px] font-bold uppercase tracking-[0.24em] text-cyan-400">Certificate A · read only</p>
            <div className="mt-2 flex items-start justify-between gap-3">
              <div><h1 className="flex items-center gap-2 text-xl font-bold text-slate-100"><FileCheck2 aria-hidden="true" className="h-5 w-5 text-cyan-400" /> Sealed evidence</h1><p className="mt-2 text-xs leading-5 text-slate-500">Inspect what the verifier observed. Nothing on this screen can authorize, queue, or apply metadata.</p></div>
              <span className="rounded-full border border-slate-700 bg-slate-950/60 px-2.5 py-1 font-mono text-xs text-slate-400">{total}</span>
            </div>
          </div>
          {runId && <div className="rounded-lg border border-cyan-900/50 bg-cyan-950/20 p-3 text-xs text-cyan-200"><span className="block text-[10px] uppercase tracking-wider text-cyan-500">Run filter</span><span className="mt-1 block break-all font-mono">{runId}</span></div>}
          <label className="block text-xs font-semibold text-slate-400">Identity tier<select value={tier} onChange={(event) => setTier(event.target.value as 'all' | IdentityTier)} className="mt-2 w-full rounded-xl border border-slate-700 bg-slate-950/70 px-3 py-2.5 text-sm text-slate-200 outline-none focus:border-cyan-500"><option value="all">All tiers</option><option value="A">Tier A — exact</option><option value="B">Tier B — review</option><option value="C">Tier C — blocked</option></select></label>
        </header>

        <div className="max-h-[38vh] overflow-y-auto lg:max-h-[calc(100vh-16rem)]">
          {listQuery.isLoading ? <div className="flex justify-center p-12" role="status" aria-label="Loading evidence"><Loader2 aria-hidden="true" className="h-6 w-6 animate-spin text-cyan-400" /></div> : listQuery.isError ? <p className="p-6 text-sm text-rose-300">Evidence could not be loaded.</p> : items.length === 0 ? <p className="p-10 text-center text-sm text-slate-500">No sealed Certificate A evidence matches this filter.</p> : (
            <div className="divide-y divide-slate-800/60">{items.map((item) => (
              <button key={item.evidence_id} type="button" onClick={() => navigate(`/review/${encodeURIComponent(item.evidence_id)}${runId ? `?run_id=${encodeURIComponent(runId)}` : ''}`)} className={clsx('w-full border-l-2 p-4 text-left transition-colors', item.evidence_id === evidenceId ? 'border-cyan-400 bg-cyan-950/20' : 'border-transparent hover:bg-slate-900/60')}>
                <div className="flex items-start justify-between gap-3"><div className="min-w-0"><p className="truncate text-sm font-semibold text-slate-200">{titleOf(item.current_metadata)}</p><p className="mt-1 truncate text-xs text-slate-500">{authorsOf(item.current_metadata)}</p></div><span className={clsx('shrink-0 rounded border px-2 py-0.5 font-mono text-[10px]', TIER_STYLE[item.tier])}>Tier {item.tier}</span></div>
                <p className="mt-3 font-mono text-[10px] text-slate-500">{item.state.replaceAll('_', ' ')} · {item.patch_fields.length} observed differences</p>
                {item.risk_flags.length > 0 && <p className="mt-2 truncate text-[11px] text-rose-400">{item.risk_flags.join(', ')}</p>}
              </button>
            ))}</div>
          )}
        </div>

        {total > 50 && <footer className="flex items-center justify-between border-t border-slate-800 p-3 text-xs"><button type="button" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 50))} className="rounded-lg px-3 py-2 text-slate-300 disabled:opacity-30">Previous</button><span className="text-slate-500">{offset + 1}–{Math.min(offset + 50, total)}</span><button type="button" disabled={offset + 50 >= total} onClick={() => setOffset(offset + 50)} className="rounded-lg px-3 py-2 text-slate-300 disabled:opacity-30">Next</button></footer>}
      </aside>

      <main className="min-w-0 flex-1 p-6 md:p-8 lg:p-10">
        {!evidenceId ? (
          <div className="mx-auto mt-20 max-w-lg text-center"><ShieldCheck aria-hidden="true" className="mx-auto h-12 w-12 text-cyan-500/60" /><h2 className="mt-5 text-xl font-bold text-slate-200">Choose a sealed evidence package</h2><p className="mt-2 text-sm leading-6 text-slate-500">You can inspect hashes, exact manifestation identifiers, provider observations, and the metadata differences the shadow policy calculated.</p></div>
        ) : detailQuery.isLoading ? (
          <div className="flex items-center gap-3 text-sm text-slate-400" role="status"><Loader2 aria-hidden="true" className="h-5 w-5 animate-spin" /> Verifying evidence seal…</div>
        ) : detailQuery.isError || !pkg ? (
          <div className="flex items-center gap-3 rounded-xl border border-rose-800/50 bg-rose-950/25 p-5 text-sm text-rose-300"><AlertTriangle aria-hidden="true" className="h-5 w-5" /> Evidence is unavailable or failed integrity validation.</div>
        ) : (
          <article className="mx-auto max-w-5xl space-y-7">
            <header className="space-y-4 border-b border-slate-800 pb-6">
              <div className="flex flex-wrap items-start justify-between gap-4"><div><p className="text-xs uppercase tracking-wider text-slate-500">Current Calibre record</p><h2 className="mt-2 text-3xl font-extrabold tracking-tight text-slate-50">{titleOf(pkg.snapshot.current_metadata)}</h2><p className="mt-2 text-sm text-slate-400">{authorsOf(pkg.snapshot.current_metadata)}</p></div><span className={clsx('rounded-xl border px-3 py-2 font-mono text-sm', TIER_STYLE[pkg.identity.tier])}>Tier {pkg.identity.tier}</span></div>
              <div className="flex flex-wrap gap-2 font-mono text-[11px] text-slate-500"><span className="rounded-lg border border-slate-800 px-2.5 py-1">{pkg.state}</span><span className="rounded-lg border border-slate-800 px-2.5 py-1">{pkg.book_key}</span><span className="rounded-lg border border-slate-800 px-2.5 py-1">{new Date(pkg.created_at).toLocaleString()}</span></div>
            </header>

            <section aria-labelledby="boundary-heading" className="rounded-2xl border border-emerald-800/50 bg-emerald-950/20 p-5"><h3 id="boundary-heading" className="flex items-center gap-2 font-bold text-emerald-200"><LockKeyhole aria-hidden="true" className="h-5 w-5" /> Read-only production boundary</h3><p className="mt-2 text-sm leading-6 text-emerald-100/70">This is a sealed observation from a shadow run. Authorization and operations are absent, and writes are disabled by the API contract.</p></section>

            <section aria-labelledby="identity-heading" className="glass-card rounded-2xl p-6"><h3 id="identity-heading" className="flex items-center gap-2 text-lg font-bold text-slate-100"><Fingerprint aria-hidden="true" className="h-5 w-5 text-cyan-400" /> Manifestation identity</h3><dl className="mt-5 grid gap-3 sm:grid-cols-2">{Object.entries(pkg.identity.manifestation_ids).map(([kind, value]) => <div key={kind} className="rounded-xl border border-slate-800 bg-slate-950/35 p-4"><dt className="text-xs uppercase tracking-wider text-slate-500">{kind}</dt><dd className="mt-2 break-all font-mono text-sm text-slate-200">{value}</dd></div>)}{Object.keys(pkg.identity.manifestation_ids).length === 0 && <p className="text-sm text-slate-500">No exact manifestation identifier was established.</p>}</dl>{pkg.identity.reasons.length > 0 && <ul className="mt-4 space-y-2 text-sm text-slate-400">{pkg.identity.reasons.map((reason) => <li key={reason} className="flex gap-2"><CheckCircle2 aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0 text-cyan-500" />{reason}</li>)}</ul>}</section>

            <section aria-labelledby="formats-heading" className="space-y-3"><h3 id="formats-heading" className="flex items-center gap-2 text-lg font-bold text-slate-100"><FileKey2 aria-hidden="true" className="h-5 w-5 text-cyan-400" /> Format inventory</h3>{pkg.formats.map((format) => <div key={`${format.path}:${format.sha256}`} className="glass-card rounded-2xl p-5"><div className="flex flex-wrap items-start justify-between gap-3"><div><p className="font-bold text-slate-200">{format.format}</p><p className="mt-1 break-all font-mono text-xs text-slate-500">{format.path}</p></div><span className="rounded-full border border-slate-700 px-2.5 py-1 text-xs text-slate-300">{format.status}</span></div><p className="mt-4 flex items-start gap-2 break-all font-mono text-xs text-slate-400"><Hash aria-hidden="true" className="mt-0.5 h-4 w-4 shrink-0 text-cyan-500" />{format.sha256}</p>{format.error && <p className="mt-3 text-sm text-rose-300">{format.error}</p>}</div>)}</section>

            <section aria-labelledby="sources-heading" className="space-y-3"><h3 id="sources-heading" className="text-lg font-bold text-slate-100">Source evidence</h3>{pkg.source_evidence.length === 0 ? <p className="text-sm text-slate-500">No source evidence was retained.</p> : <div className="grid gap-3">{pkg.source_evidence.map((source) => <div key={source.evidence_id} className="glass-card rounded-2xl p-5"><div className="flex flex-wrap items-start justify-between gap-3"><div><p className="font-semibold text-slate-200">{source.field}: <span className="font-normal text-cyan-200">{valueOf(source.value)}</span></p><p className="mt-1 text-xs text-slate-500">{source.source_kind} · root {source.root_id}</p></div>{source.authoritative && <span className="rounded-full border border-emerald-700/60 bg-emerald-950/30 px-2.5 py-1 text-[10px] uppercase tracking-wider text-emerald-300">authoritative</span>}</div>{source.locator && <p className="mt-3 text-sm text-slate-400">{source.locator}</p>}<p className="mt-3 break-all font-mono text-[10px] text-slate-600">{source.evidence_id}</p></div>)}</div>}</section>

            <section aria-labelledby="differences-heading" className="space-y-3"><h3 id="differences-heading" className="text-lg font-bold text-slate-100">Shadow metadata differences</h3><p className="text-sm text-slate-500">Calculated observations only. Certificate A cannot apply them.</p>{patchEntries.length === 0 ? <p className="rounded-xl border border-slate-800 p-5 text-sm text-slate-500">No safe patch was calculated for this evidence package.</p> : <div className="overflow-x-auto rounded-2xl border border-slate-800"><table className="w-full text-left text-sm"><thead className="bg-slate-950/70 text-xs uppercase tracking-wider text-slate-500"><tr><th className="px-4 py-3">Field</th><th className="px-4 py-3">Current</th><th className="px-4 py-3">Observed</th></tr></thead><tbody className="divide-y divide-slate-800">{patchEntries.map(([field, observed]) => <tr key={field}><th className="px-4 py-3 font-mono text-xs text-slate-400">{field}</th><td className="px-4 py-3 text-slate-400">{valueOf(currentValue(pkg, field))}</td><td className="px-4 py-3 text-cyan-200">{valueOf(observed)}</td></tr>)}</tbody></table></div>}</section>

            {(pkg.identity.risk_flags.length > 0 || pkg.warnings.length > 0 || pkg.error) && <section aria-labelledby="warnings-heading" className="rounded-2xl border border-amber-800/50 bg-amber-950/20 p-5"><h3 id="warnings-heading" className="flex items-center gap-2 font-bold text-amber-200"><AlertTriangle aria-hidden="true" className="h-5 w-5" /> Risks and warnings</h3><ul className="mt-3 space-y-2 text-sm text-amber-100/75">{[...pkg.identity.risk_flags, ...pkg.warnings, ...(pkg.error ? [pkg.error] : [])].map((warning) => <li key={warning}>{warning}</li>)}</ul></section>}

            <footer className="space-y-2 border-t border-slate-800 pt-5 font-mono text-[10px] text-slate-600"><p className="break-all">Evidence: {pkg.evidence_id}</p><p className="break-all">Snapshot SHA-256: {pkg.snapshot.snapshot_sha256}</p><p className="break-all">Package SHA-256: {pkg.package_sha256}</p></footer>
          </article>
        )}
      </main>
    </div>
  )
}
