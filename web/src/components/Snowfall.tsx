import { useMemo, type CSSProperties } from 'react'
import { cn } from '@/lib/utils'

type FlakeStyle = CSSProperties & { '--flake-drift': string; '--flake-spin': string; '--flake-opacity': number }

const FLAKE_COUNT = 42

interface Flake {
  id: number
  left: number
  size: number
  delay: number
  duration: number
  drift: number
  spin: number
  opacity: number
  blur: boolean
}

// Three depths: small flakes are far away, so they fall slower, dimmer and
// out of focus; the few large ones pass close and sharp.
function createFlakes(): Flake[] {
  return Array.from({ length: FLAKE_COUNT }, (_, id) => {
    const depth = Math.random()
    const near = depth > 0.82
    const far = depth < 0.45
    return {
      id,
      left: Math.random() * 100,
      size: near ? 18 + Math.random() * 10 : far ? 5 + Math.random() * 4 : 10 + Math.random() * 6,
      delay: -Math.random() * 24,
      duration: near ? 11 + Math.random() * 4 : far ? 22 + Math.random() * 8 : 15 + Math.random() * 5,
      drift: (Math.random() - 0.5) * (near ? 90 : 40),
      spin: (Math.random() - 0.5) * 540,
      opacity: near ? 0.9 : far ? 0.45 : 0.7,
      blur: far,
    }
  })
}

/** Snow falling across its box; decoration only, and still when motion is reduced. */
export default function Snowfall({ className }: { className?: string }) {
  const flakes = useMemo(createFlakes, [])
  return (
    <div className={cn('pointer-events-none overflow-hidden', className)} aria-hidden="true">
      <svg className="absolute size-0">
        <symbol id="tw-flake" viewBox="0 0 24 24">
          <g fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
            {[0, 60, 120].map((deg) => (
              <path
                key={deg}
                transform={`rotate(${deg} 12 12)`}
                d="M12 1.5v21M9.3 3.6 12 6.3l2.7-2.7M9.3 20.4 12 17.7l2.7 2.7"
              />
            ))}
          </g>
        </symbol>
      </svg>
      {flakes.map((f) => {
        const style: FlakeStyle = {
          left: `${f.left}%`,
          width: f.size,
          height: f.size,
          animationDelay: `${f.delay}s`,
          animationDuration: `${f.duration}s`,
          filter: f.blur ? 'blur(0.6px)' : undefined,
          '--flake-drift': `${f.drift}px`,
          '--flake-spin': `${f.spin}deg`,
          '--flake-opacity': f.opacity,
        }
        return (
          <svg key={f.id} className="snowflake" style={style}>
            <use href="#tw-flake" />
          </svg>
        )
      })}
    </div>
  )
}
