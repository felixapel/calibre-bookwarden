import React, { createContext, useContext, useState, useCallback, useEffect } from 'react'
import { X, CheckCircle, AlertTriangle, AlertCircle, Info } from 'lucide-react'

export type ToastType = 'success' | 'error' | 'info' | 'warning'

export interface Toast {
  id: string
  message: string
  type: ToastType
}

interface ToastContextType {
  showToast: (message: string, type?: ToastType) => void
}

const ToastContext = createContext<ToastContextType | undefined>(undefined)

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])

  const showToast = useCallback((message: string, type: ToastType = 'info') => {
    const id = Math.random().toString(36).substring(2, 9)
    setToasts((prev) => [...prev, { id, message, type }])
  }, [])

  const removeToast = useCallback((id: string) => {
    setToasts((prev) => prev.filter((t) => t.id !== id))
  }, [])

  return (
    <ToastContext.Provider value={{ showToast }}>
      {children}
      {/* Toast container */}
      <div
        aria-live="polite"
        aria-atomic="false"
        className="fixed bottom-5 right-5 z-50 flex flex-col gap-3 max-w-sm w-full pointer-events-none"
      >
        {toasts.map((toast) => {
          let Icon = Info
          let colorClass = 'border-blue-500/20 bg-slate-900/90 text-blue-300'
          
          if (toast.type === 'success') {
            Icon = CheckCircle
            colorClass = 'border-emerald-500/20 bg-slate-900/90 text-emerald-400'
          } else if (toast.type === 'error') {
            Icon = AlertCircle
            colorClass = 'border-rose-500/20 bg-slate-900/90 text-rose-400'
          } else if (toast.type === 'warning') {
            Icon = AlertTriangle
            colorClass = 'border-amber-500/20 bg-slate-900/90 text-amber-400'
          }

          return (
            <ToastItem
              key={toast.id}
              toast={toast}
              colorClass={colorClass}
              icon={<Icon className="w-5 h-5 shrink-0" />}
              onClose={removeToast}
            />
          )
        })}
      </div>
    </ToastContext.Provider>
  )
}

function ToastItem({ 
  toast, 
  colorClass, 
  icon, 
  onClose 
}: { 
  toast: Toast
  colorClass: string
  icon: React.ReactNode
  onClose: (id: string) => void 
}) {
  useEffect(() => {
    const timer = setTimeout(() => {
      onClose(toast.id)
    }, 4000)
    return () => clearTimeout(timer)
  }, [toast.id, onClose])

  return (
    <div
      className={`flex items-start gap-3 p-4 rounded-xl border backdrop-blur-md shadow-2xl pointer-events-auto transition-all duration-300 animate-in slide-in-from-right-5 fade-in ${colorClass}`}
    >
      <div className="mt-0.5">{icon}</div>
      <div className="flex-1 text-sm font-medium leading-5 select-none">{toast.message}</div>
      <button
        aria-label="Dismiss notification"
        onClick={() => onClose(toast.id)}
        className="text-slate-400 hover:text-slate-200 transition-colors p-0.5 rounded cursor-pointer"
      >
        <X className="w-4 h-4" />
      </button>
    </div>
  )
}

export function useToast() {
  const context = useContext(ToastContext)
  if (!context) {
    throw new Error('useToast must be used within a ToastProvider')
  }
  return context
}
