/**
 * Fills the odd cell in the budgets grid.
 *
 * A two-column grid with three cards leaves a hole, and a hole reads as a card
 * that failed to load. It carries no data on purpose: everything on this screen
 * is a figure somebody has to trust, and inventing a fourth panel of numbers to
 * balance a layout is how a dashboard starts lying politely.
 */
export function BudgetPoster() {
  return (
    <div className="relative isolate hidden min-h-52 overflow-hidden rounded-xl border border-line-soft lg:block">
      <svg
        aria-hidden="true"
        viewBox="0 0 400 220"
        preserveAspectRatio="xMidYMid slice"
        className="absolute inset-0 -z-10 size-full"
      >
        <defs>
          <linearGradient id="poster-sky" x1="0" y1="0" x2="0.4" y2="1">
            <stop stopColor="#241a48" />
            <stop offset="0.55" stopColor="#1a1230" />
            <stop offset="1" stopColor="#100b1e" />
          </linearGradient>
          <radialGradient id="poster-glow">
            <stop stopColor="#e8dcff" stopOpacity="0.5" />
            <stop offset="0.5" stopColor="#9b7ef0" stopOpacity="0.18" />
            <stop offset="1" stopColor="#9b7ef0" stopOpacity="0" />
          </radialGradient>
        </defs>
        <rect width="400" height="220" fill="url(#poster-sky)" />
        <circle cx="286" cy="70" r="92" fill="url(#poster-glow)" />
        <circle cx="286" cy="70" r="40" fill="#efe9ff" fillOpacity="0.9" />
        <circle cx="268" cy="58" r="40" fill="url(#poster-sky)" />
        <path d="M0 168 62 128l52 30 60-44 66 46 54-30 106 54v36H0Z" fill="#241a40" />
        <path d="M0 196 78 166l64 22 62-16 70 26 126-20v22H0Z" fill="#120c22" />
      </svg>

      <p className="absolute bottom-8 left-6 max-w-[14rem] font-serif text-[21px] leading-[1.14] text-fg">
        Allocate today. Create tomorrow.
      </p>
      <span aria-hidden="true" className="absolute bottom-5 left-6 h-px w-8 bg-fg/35" />
    </div>
  );
}
