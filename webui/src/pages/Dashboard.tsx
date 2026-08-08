import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { BookOpenCheck, Database, Eye, LockKeyhole, ScanSearch } from 'lucide-react'

import { fetchCapabilities, fetchHealth, fetchVerifyRuns } from '../api/client'

export default function Dashboard() {
  const health = useQuery({ queryKey: ['health'], queryFn: fetchHealth, retry: false })
  const capabilities = useQuery({ queryKey: ['capabilities'], queryFn: fetchCapabilities })
  const runs = useQuery({ queryKey: ['verifyRuns'], queryFn: fetchVerifyRuns })
  const recent = runs.data?.runs[0]

  return (
    <div className="mx-auto max-w-6xl space-y-8 p-6 md:p-10">
      <header className="space-y-3 border-b border-slate-800/60 pb-7">
        <div className="flex items-center gap-3 text-cyan-300">
          <BookOpenCheck aria-hidden="true" className="h-7 w-7" />
          <span className="text-xs font-bold uppercase tracking-[0.25em]">Production Certificate A</span>
        </div>
        <h1 className="text-3xl font-extrabold tracking-tight text-slate-50 md:text-4xl">Audit your Calibre metadata without touching the library</h1>
        <p className="max-w-3xl text-sm leading-6 text-slate-400">
          The verifier takes a frozen, read-only snapshot after you confirm Calibre is stopped. Every result is sealed evidence. Writes, uploads, LLMs, and legacy jobs are unavailable in this release.
        </p>
      </header>

      <section aria-label="Safety status" className="grid gap-4 md:grid-cols-3">
        <StatusCard icon={Database} title="Verifier" value={health.data?.status === 'ready' ? 'Ready' : 'Unavailable'} detail="Separate worker · PostgreSQL fenced" />
        <StatusCard icon={Eye} title="Mode" value="Shadow only" detail="No Calibre metadata writes" />
        <StatusCard icon={LockKeyhole} title="Library access" value="Read-only" detail="Mounted only into the verifier" />
      </section>

      <section className="grid gap-6 lg:grid-cols-[1.4fr_1fr]">
        <div className="glass-card space-y-5 rounded-2xl p-6">
          <h2 className="text-lg font-bold text-slate-100">Safe run sequence</h2>
          <ol className="space-y-4 text-sm text-slate-300">
            {[
              ['1', 'Stop Calibre', 'Close the desktop app and Content Server so metadata.db and book files cannot change.'],
              ['2', 'Request a bounded run', 'Choose a book limit, confirm the stop, and optionally enable bounded Tesseract OCR.'],
              ['3', 'Review sealed evidence', 'Inspect exact ISBN provider evidence and conflicts. Certificate A never queues a write.'],
            ].map(([number, title, detail]) => (
              <li key={number} className="flex gap-4">
                <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-cyan-500/15 font-mono text-cyan-300">{number}</span>
                <div><p className="font-semibold text-slate-100">{title}</p><p className="mt-1 text-slate-400">{detail}</p></div>
              </li>
            ))}
          </ol>
          <Link to="/verify" className="inline-flex items-center gap-2 rounded-xl bg-cyan-600 px-5 py-3 text-sm font-bold text-white hover:bg-cyan-500">
            <ScanSearch aria-hidden="true" className="h-4 w-4" />
            Start a shadow audit
          </Link>
        </div>

        <div className="glass-card space-y-4 rounded-2xl p-6">
          <h2 className="text-lg font-bold text-slate-100">Release boundary</h2>
          <dl className="space-y-3 text-sm">
            <Row label="Pipeline" value={capabilities.data?.pipeline ?? 'manifestation-v2'} />
            <Row label="Providers" value={capabilities.data?.providers.join(' + ') ?? 'Loading…'} />
            <Row label="OCR" value={capabilities.data?.ocr.enabled ? `Tesseract · ${capabilities.data.ocr.max_pages} pages` : 'Disabled'} />
            <Row label="Writes" value="Disabled" />
          </dl>
          <div className="border-t border-slate-800 pt-4">
            <p className="text-xs uppercase tracking-wider text-slate-400">Most recent run</p>
            <p className="mt-2 break-all font-mono text-xs text-slate-300">{recent?.run_id ?? 'No runs yet'}</p>
            {recent && <p className="mt-1 text-xs text-slate-500">{recent.status} · {recent.completed}/{recent.total ?? '?'}</p>}
          </div>
        </div>
      </section>
    </div>
  )
}

function StatusCard({ icon: Icon, title, value, detail }: { icon: typeof Database; title: string; value: string; detail: string }) {
  return (
    <div className="glass-card rounded-2xl p-5">
      <Icon aria-hidden="true" className="h-5 w-5 text-cyan-400" />
      <p className="mt-4 text-xs uppercase tracking-wider text-slate-400">{title}</p>
      <p className="mt-1 text-lg font-bold text-slate-100">{value}</p>
      <p className="mt-1 text-xs text-slate-400">{detail}</p>
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return <div className="flex items-start justify-between gap-4"><dt className="text-slate-400">{label}</dt><dd className="text-right text-slate-200">{value}</dd></div>
}
