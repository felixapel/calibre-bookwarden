import { useState, useEffect } from 'react'
import { BookOpen, Loader2 } from 'lucide-react'
import clsx from 'clsx'
import { getApiKey } from '../api/auth'

interface CoverImageProps {
  bookKey: string
  className?: string
}

export default function CoverImage({ bookKey, className }: CoverImageProps) {
  const [imgUrl, setImgUrl] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  const [authVersion, setAuthVersion] = useState(0)

  useEffect(() => {
    const retry = () => setAuthVersion((value) => value + 1)
    window.addEventListener('bookaudit-authenticated', retry)
    return () => window.removeEventListener('bookaudit-authenticated', retry)
  }, [])

  useEffect(() => {
    if (!bookKey) {
      setLoading(false)
      setError(true)
      return
    }

    const cleanKey = bookKey.replace(':', '_')
    const url = `/api/covers/${cleanKey}.jpg`
    const apiKey = getApiKey()
    
    let active = true
    let objectUrl: string | null = null
    setLoading(true)
    setError(false)

    const fetchImage = async () => {
      try {
        const headers: HeadersInit = {}
        if (apiKey) {
          headers['X-API-Key'] = apiKey
        }

        const res = await fetch(url, { headers })
        if (!res.ok) {
          throw new Error('Failed to load image')
        }

        const blob = await res.blob()
        if (active) {
          objectUrl = URL.createObjectURL(blob)
          setImgUrl(objectUrl)
          setLoading(false)
        }
      } catch (e) {
        if (active) {
          setError(true)
          setLoading(false)
        }
      }
    }

    fetchImage()

    return () => {
      active = false
      if (objectUrl) {
        URL.revokeObjectURL(objectUrl)
      }
    }
  }, [bookKey, authVersion])

  if (loading) {
    return (
      <div className={clsx("flex items-center justify-center bg-slate-950/40 border border-slate-900 rounded-xl", className)}>
        <Loader2 className="w-5 h-5 text-purple-500 animate-spin" />
      </div>
    )
  }

  if (error || !imgUrl) {
    return (
      <div className={clsx("flex flex-col items-center justify-center bg-slate-950/40 border border-slate-900 rounded-xl text-slate-600", className)}>
        <BookOpen className="w-6 h-6 stroke-[1.5]" />
      </div>
    )
  }

  return (
    <img 
      src={imgUrl} 
      alt="Book cover" 
      className={clsx("object-cover rounded-xl border border-slate-800/40 shadow-md", className)} 
    />
  )
}
