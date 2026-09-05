import type { ReactElement } from "react";

/**
 * The scene at the foot of the sidebar.
 *
 * Drawn as SVG rather than shipped as images. Four full-bleed renders would be
 * several times the weight of this entire application, would need a decode on
 * every navigation, and would have to be art-directed for three breakpoints. A
 * few dozen paths cost about two kilobytes and stay sharp at any density.
 *
 * Three things make it read as a landscape rather than as a pattern, and all
 * three are what a photograph of one does. Ranges get *lighter and flatter* as
 * they recede, because there is more air in front of them - so the nearest
 * silhouette is almost black and the furthest is nearly the colour of the sky.
 * Blurred bands of that sky sit between them, which is the air itself. And no
 * two peaks are the same width or angle: regular spacing is the single thing
 * that turns a mountain range into wallpaper.
 *
 * It is decoration, so it is hidden from the accessible tree - and the line of
 * text over it is a mood, not information. Nothing here is the only place
 * anything is said.
 */
export type Scene = "ridge" | "arch" | "road" | "crescent";

/** Snow catching the last light over a violet valley: the expense list. */
function Ridge(): ReactElement {
  return (
    <>
      <rect width="240" height="360" fill="url(#sky-dawn)" />
      <circle cx="168" cy="104" r="96" fill="url(#glow-warm)" />

      <g fill="#7c68b4" opacity="0.34">
        <path d="M0 228 26 200l22 16 30-38 26 30 22-14 34 36 28-20 52 46v82H0Z" />
      </g>
      <rect y="196" width="240" height="56" fill="url(#haze)" filter="url(#soften)" />

      <g fill="#4a3a78" opacity="0.72">
        <path d="M0 252 30 218l18 14 44-58 30 42 18-12 40 44 24-16 36 34v86H0Z" />
      </g>
      {/* The lit face of the main summit, on the side the glow is on. */}
      <path d="m92 174 30 42-22 18-20-30Z" fill="#b6a3e4" opacity="0.5" />
      <rect y="240" width="240" height="48" fill="url(#haze)" filter="url(#soften)" opacity="0.7" />

      <path
        d="M0 286 22 262l16 12 34-46 22 30 16-10 26 32 20-14 30 26 24-16 30 30v106H0Z"
        fill="#1c1436"
      />
      <path d="m72 228 22 30-16 14-14-22Z" fill="#8e7cc8" opacity="0.34" />
      <path d="M0 318 34 296l26 16 22-12 30 20 26-14 36 22 32-12 34 18v42H0Z" fill="#0e0a1c" />
      <rect y="296" width="240" height="64" fill="url(#floor)" />
    </>
  );
}

/** A monolith with light standing in it, and a path to it: the queue. */
function Arch(): ReactElement {
  return (
    <>
      <rect width="240" height="360" fill="url(#sky-cool)" />
      <circle cx="120" cy="196" r="104" fill="url(#glow-cool)" />

      <g fill="#6f5ea8" opacity="0.3">
        <path d="M0 226 28 202l20 14 32-40 26 32 20-14 30 34 26-18 58 44v88H0Z" />
      </g>
      <rect y="200" width="240" height="52" fill="url(#haze)" filter="url(#soften)" />

      <path d="M98 302V196a22 22 0 0 1 44 0v106Z" fill="url(#shaft)" />
      <path d="M76 302V194a44 44 0 0 1 88 0v108h-22V194a22 22 0 0 0-44 0v108Z" fill="#241a46" />
      {/* One face of the monolith takes the light, which is what stops it
          reading as a flat cut-out. */}
      <path d="M76 302V194a44 44 0 0 1 21-37v145Z" fill="#3a2b68" />

      <path
        d="M0 278 26 258l18 12 30-32 24 24 18-12 28 26 22-14 34 26 40-22v82H0Z"
        fill="#1a1230"
      />
      <path d="M110 360c2-30 6-44 12-56h-4c-9 14-15 28-16 56Z" fill="#cbbcf4" opacity="0.3" />
      <path d="M0 320 40 300l30 16 28-12 32 18 30-12 40 18 40-14v34H0Z" fill="#0d0919" />
      <rect y="300" width="240" height="60" fill="url(#floor)" />
    </>
  );
}

/** A lit ribbon threading dark hills: the budgets. */
function Road(): ReactElement {
  return (
    <>
      <rect width="240" height="360" fill="url(#sky-ember)" />
      <circle cx="146" cy="156" r="96" fill="url(#glow-warm)" />

      <g fill="#7a5f92" opacity="0.28">
        <path d="M0 234 30 208l20 16 34-36 26 28 24-16 34 32 30-20 42 38v90H0Z" />
      </g>
      <rect y="212" width="240" height="52" fill="url(#haze)" filter="url(#soften)" />

      <path
        d="M0 268 28 244l22 16 36-32 26 26 20-14 32 28 26-18 50 34v78H0Z"
        fill="#251b3c"
      />

      {/* One curve, three passes: bloom, ribbon, and the lit edge. */}
      <g strokeLinecap="round" fill="none">
        <path d="M-18 376c58-72 132-74 160-132s-22-94 32-140" stroke="url(#ember)" strokeWidth="32" strokeOpacity="0.22" />
        <path d="M-18 376c58-72 132-74 160-132s-22-94 32-140" stroke="url(#ember)" strokeWidth="9" />
        <path d="M-18 376c58-72 132-74 160-132s-22-94 32-140" stroke="#ffe4d0" strokeWidth="2" strokeOpacity="0.8" />
      </g>

      <path d="M0 314 38 294l30 18 30-14 34 20 30-14 40 20 38-16v52H0Z" fill="#100b1e" />
      <rect y="296" width="240" height="64" fill="url(#floor)" />
    </>
  );
}

/** A crescent behind faceted peaks: the organisation, and the default. */
function Crescent(): ReactElement {
  return (
    <>
      <rect width="240" height="360" fill="url(#sky-night)" />
      <circle cx="152" cy="116" r="98" fill="url(#glow-cool)" />

      <circle cx="152" cy="116" r="42" fill="#f0eaff" fillOpacity="0.94" />
      <circle cx="133" cy="103" r="42" fill="url(#sky-night)" />

      <g fill="#6a58a6" opacity="0.3">
        <path d="M0 246 28 222l22 14 30-34 28 28 20-14 34 30 28-18 50 40v92H0Z" />
      </g>
      <rect y="224" width="240" height="52" fill="url(#haze)" filter="url(#soften)" />

      {/* Faceted rather than smooth: each face is its own flat tone, which is
          what reads as crystal instead of as hill. */}
      <path d="M0 296 44 224l24 40 34-66 40 74 26-28 40 48 32-24v92H0Z" fill="#1e1638" />
      <path d="m102 198 40 74-26 20-26-52Z" fill="#5c4c96" opacity="0.62" />
      <path d="m44 224 24 40-22 20-20-32Z" fill="#493c7c" opacity="0.55" />
      <path d="m182 264 32-24 26 30v30l-40-12Z" fill="#42356e" opacity="0.5" />
      <path d="M0 326 46 304l34 18 30-16 36 20 30-14 40 18 24-10v50H0Z" fill="#0d091b" />
      <rect y="306" width="240" height="54" fill="url(#floor)" />
    </>
  );
}

const SCENES: Record<Scene, () => ReactElement> = {
  ridge: Ridge,
  arch: Arch,
  road: Road,
  crescent: Crescent,
};

export function RailArt({ scene, caption }: { scene: Scene; caption: string }) {
  const Painting = SCENES[scene];

  return (
    <div className="relative isolate mt-auto min-h-[24rem] overflow-hidden">
      <svg
        aria-hidden="true"
        viewBox="0 0 240 360"
        preserveAspectRatio="xMidYMax slice"
        className="absolute inset-0 -z-10 size-full"
      >
        <defs>
          <linearGradient id="sky-dawn" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#2d1e56" />
            <stop offset="0.45" stopColor="#3b2662" />
            <stop offset="1" stopColor="#150e29" />
          </linearGradient>
          <linearGradient id="sky-cool" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#1e1544" />
            <stop offset="0.5" stopColor="#2b1d57" />
            <stop offset="1" stopColor="#120c24" />
          </linearGradient>
          <linearGradient id="sky-ember" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#271946" />
            <stop offset="0.5" stopColor="#352142" />
            <stop offset="1" stopColor="#140d25" />
          </linearGradient>
          <linearGradient id="sky-night" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#241853" />
            <stop offset="0.5" stopColor="#2c1e5a" />
            <stop offset="1" stopColor="#130c27" />
          </linearGradient>

          <radialGradient id="glow-warm">
            <stop stopColor="#ffdcc0" stopOpacity="0.44" />
            <stop offset="0.45" stopColor="#c9a6f0" stopOpacity="0.2" />
            <stop offset="1" stopColor="#c9a6f0" stopOpacity="0" />
          </radialGradient>
          <radialGradient id="glow-cool">
            <stop stopColor="#e8dcff" stopOpacity="0.46" />
            <stop offset="0.45" stopColor="#9b7ef0" stopOpacity="0.18" />
            <stop offset="1" stopColor="#9b7ef0" stopOpacity="0" />
          </radialGradient>

          {/* The air between the ranges, actually blurred rather than merely
              faded: a hard-edged band reads as a stripe, not as distance. */}
          <filter id="soften" x="-20%" y="-60%" width="140%" height="220%">
            <feGaussianBlur stdDeviation="9" />
          </filter>
          <linearGradient id="haze" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#9985cf" stopOpacity="0" />
            <stop offset="0.5" stopColor="#9985cf" stopOpacity="0.34" />
            <stop offset="1" stopColor="#9985cf" stopOpacity="0" />
          </linearGradient>

          {/* Every scene resolves to the sidebar colour at its foot, which is
              what makes the caption legible without a scrim over the art. */}
          <linearGradient id="floor" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#07050d" stopOpacity="0" />
            <stop offset="0.5" stopColor="#07050d" stopOpacity="0.86" />
            <stop offset="1" stopColor="#07050d" />
          </linearGradient>

          <linearGradient id="shaft" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#f4ecff" stopOpacity="0.92" />
            <stop offset="0.6" stopColor="#b79cf5" stopOpacity="0.32" />
            <stop offset="1" stopColor="#8b6fe8" stopOpacity="0.04" />
          </linearGradient>
          <linearGradient id="ember" x1="0" y1="1" x2="1" y2="0">
            <stop stopColor="#ff9a63" stopOpacity="0.95" />
            <stop offset="0.6" stopColor="#c98ae0" stopOpacity="0.48" />
            <stop offset="1" stopColor="#8b6fe8" stopOpacity="0.08" />
          </linearGradient>
        </defs>
        <Painting />
      </svg>

      <p className="absolute right-5 bottom-9 left-5 font-serif text-[21px] leading-[1.14] text-fg">
        {caption}
      </p>
      <span aria-hidden="true" className="absolute bottom-5 left-5 h-px w-8 bg-fg/35" />
    </div>
  );
}
