import type { ReactElement } from "react";

/**
 * The scene at the foot of the sidebar.
 *
 * Drawn as SVG rather than shipped as images. Four full-bleed renders would be
 * several times the weight of this entire application, would need a decode on
 * every navigation, and would have to be art-directed for three breakpoints. A
 * few dozen paths cost about two kilobytes and stay sharp at any density.
 *
 * The thing that makes it belong to the sidebar rather than sit in it: the
 * scene resolves to the rail's own colour at *both* ends. A dark landscape with
 * a hard top edge reads as a photograph someone pasted in; fading into the
 * panel above and below makes it part of the surface.
 *
 * And it is dark. Almost all of it is within a few percent of the rail, with a
 * single quiet light source and rim-light on a handful of edges. A bright field
 * of colour here would pull the eye away from the navigation, which is the only
 * thing in this column anybody came for.
 *
 * It is decoration, so it is hidden from the accessible tree - and the line of
 * text over it is a mood, not information. Nothing here is the only place
 * anything is said.
 */
export type Scene = "ridge" | "arch" | "road" | "crescent";

/** Snow catching the last light over a dark valley: the expense list. */
function Ridge(): ReactElement {
  return (
    <>
      <rect width="240" height="360" fill="url(#sky-dawn)" />
      <ellipse cx="150" cy="150" rx="118" ry="96" fill="url(#glow-warm)" />

      {/* Furthest range: barely separated from the sky it stands in. */}
      <path
        d="M0 232 26 206l22 14 30-34 26 26 22-12 34 32 28-18 52 42v100H0Z"
        fill="#3b3660"
        opacity="0.6"
      />
      <rect y="204" width="240" height="56" fill="url(#haze)" filter="url(#soften)" />

      <path
        d="M0 256 30 224l18 12 44-54 30 38 18-10 40 40 24-14 36 30v94H0Z"
        fill="#2a2648"
      />
      {/* The lit face of the main summit, on the side the glow is on. */}
      <path d="m92 182 30 38-22 16-20-28Z" fill="#c8b6ee" opacity="0.4" />
      <path d="m92 182 30 38" stroke="#e6dcff" strokeWidth="1.1" strokeOpacity="0.65" fill="none" />
      <rect y="246" width="240" height="46" fill="url(#haze)" filter="url(#soften)" opacity="0.6" />

      {/* Nearest range: almost the rail's own colour. */}
      <path
        d="M0 290 22 268l16 10 34-42 22 28 16-8 26 30 20-12 30 24 24-14 30 28v98H0Z"
        fill="#141426"
      />
      <path d="m72 236 22 28" stroke="#b9a8e4" strokeWidth="1" strokeOpacity="0.4" fill="none" />
      <path d="M0 322 34 300l26 14 22-10 30 18 26-12 36 20 32-10 34 16v42H0Z" fill="#0a0b11" />
    </>
  );
}

/** A monolith with light standing in it, and a path to it: the queue. */
function Arch(): ReactElement {
  return (
    <>
      <rect width="240" height="360" fill="url(#sky-cool)" />
      <ellipse cx="120" cy="212" rx="112" ry="112" fill="url(#glow-cool)" />

      <path
        d="M0 232 28 208l20 12 32-36 26 28 20-12 30 30 26-16 58 40v104H0Z"
        fill="#393463"
        opacity="0.55"
      />
      <rect y="208" width="240" height="52" fill="url(#haze)" filter="url(#soften)" />

      <path d="M98 304V198a22 22 0 0 1 44 0v106Z" fill="url(#shaft)" />
      <path d="M76 304V196a44 44 0 0 1 88 0v108h-20V196a24 24 0 0 0-48 0v108Z" fill="#241f42" />
      {/* One face of the monolith takes the light, which is what stops it
          reading as a flat cut-out. */}
      <path d="M76 304V196a44 44 0 0 1 20-36v144Z" fill="#332c5a" />
      <path d="M96 160v144" stroke="#cbbcf4" strokeWidth="0.9" strokeOpacity="0.45" fill="none" />

      <path
        d="M0 282 26 262l18 10 30-30 24 22 18-10 28 24 22-12 34 24 40-20v90H0Z"
        fill="#141227"
      />
      <path d="M110 360c2-28 6-42 12-54h-4c-9 14-15 26-16 54Z" fill="#cbbcf4" opacity="0.3" />
      <path d="M0 324 40 304l30 14 28-10 32 16 30-10 40 16 40-12v42H0Z" fill="#0a0b11" />
    </>
  );
}

/** A lit ribbon threading dark hills: the budgets. */
function Road(): ReactElement {
  return (
    <>
      <rect width="240" height="360" fill="url(#sky-ember)" />
      <ellipse cx="140" cy="190" rx="112" ry="100" fill="url(#glow-warm)" />

      <path
        d="M0 238 30 212l20 14 34-32 26 26 24-14 34 28 30-18 42 34v102H0Z"
        fill="#3d3358"
        opacity="0.55"
      />
      <rect y="216" width="240" height="52" fill="url(#haze)" filter="url(#soften)" />

      <path d="M0 272 28 248l22 14 36-30 26 24 20-12 32 26 26-16 50 32v92H0Z" fill="#241d38" />

      {/* One curve, three passes: bloom, ribbon, and the lit edge. */}
      <g strokeLinecap="round" fill="none">
        <path d="M-18 376c58-72 132-74 160-132s-22-94 32-140" stroke="url(#ember)" strokeWidth="30" strokeOpacity="0.16" />
        <path d="M-18 376c58-72 132-74 160-132s-22-94 32-140" stroke="url(#ember)" strokeWidth="7" strokeOpacity="0.75" />
        <path d="M-18 376c58-72 132-74 160-132s-22-94 32-140" stroke="#ffdcc4" strokeWidth="1.6" strokeOpacity="0.7" />
      </g>

      <path d="M0 318 38 298l30 16 30-12 34 18 30-12 40 18 38-14v48H0Z" fill="#0a0b11" />
    </>
  );
}

/** A crescent behind faceted peaks: the organisation, and the default. */
function Crescent(): ReactElement {
  return (
    <>
      <rect width="240" height="360" fill="url(#sky-night)" />
      <ellipse cx="150" cy="132" rx="106" ry="96" fill="url(#glow-cool)" />

      {/* Small and soft, and partly hidden by the range in front of it, which
          is what puts it in the sky rather than on top of the panel. */}
      <circle cx="150" cy="130" r="34" fill="#ece6ff" fillOpacity="0.9" />
      <circle cx="135" cy="120" r="34" fill="url(#sky-night)" />

      <path
        d="M0 250 28 226l22 12 30-32 28 26 20-12 34 28 28-16 50 38v98H0Z"
        fill="#3a3462"
        opacity="0.55"
      />
      <rect y="228" width="240" height="50" fill="url(#haze)" filter="url(#soften)" />

      {/* Faceted rather than smooth: each face is its own flat tone, which is
          what reads as crystal instead of as hill. */}
      <path d="M0 300 44 232l24 38 34-62 40 70 26-26 40 44 32-22v86H0Z" fill="#221e3c" />
      <path d="m102 208 40 70-26 18-26-48Z" fill="#645b96" opacity="0.6" />
      <path d="m44 232 24 38-22 18-20-30Z" fill="#514a82" opacity="0.55" />
      <path d="m102 208 40 70" stroke="#d7cbf7" strokeWidth="1" strokeOpacity="0.45" fill="none" />
      <path d="M0 330 46 308l34 16 30-14 36 18 30-12 40 16 24-8v46H0Z" fill="#0a0b11" />
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
            <stop stopColor="#231f3d" />
            <stop offset="0.5" stopColor="#262340" />
            <stop offset="1" stopColor="#0a0b11" />
          </linearGradient>
          <linearGradient id="sky-cool" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#1d1b3c" />
            <stop offset="0.5" stopColor="#252143" />
            <stop offset="1" stopColor="#0a0b11" />
          </linearGradient>
          <linearGradient id="sky-ember" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#261d38" />
            <stop offset="0.5" stopColor="#2e2039" />
            <stop offset="1" stopColor="#0a0b11" />
          </linearGradient>
          <linearGradient id="sky-night" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#211d40" />
            <stop offset="0.5" stopColor="#262046" />
            <stop offset="1" stopColor="#0a0b11" />
          </linearGradient>

          <radialGradient id="glow-warm">
            <stop stopColor="#ffd0ab" stopOpacity="0.45" />
            <stop offset="0.4" stopColor="#a888e8" stopOpacity="0.22" />
            <stop offset="1" stopColor="#a888e8" stopOpacity="0" />
          </radialGradient>
          <radialGradient id="glow-cool">
            <stop stopColor="#ddd0ff" stopOpacity="0.48" />
            <stop offset="0.4" stopColor="#8b6fe8" stopOpacity="0.2" />
            <stop offset="1" stopColor="#8b6fe8" stopOpacity="0" />
          </radialGradient>

          {/* The air between the ranges, actually blurred rather than merely
              faded: a hard-edged band reads as a stripe, not as distance. */}
          <filter id="soften" x="-20%" y="-60%" width="140%" height="220%">
            <feGaussianBlur stdDeviation="10" />
          </filter>
          <linearGradient id="haze" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#8f8ac0" stopOpacity="0" />
            <stop offset="0.5" stopColor="#8f8ac0" stopOpacity="0.2" />
            <stop offset="1" stopColor="#8f8ac0" stopOpacity="0" />
          </linearGradient>

          <linearGradient id="shaft" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#efe8ff" stopOpacity="0.8" />
            <stop offset="0.6" stopColor="#a98ff0" stopOpacity="0.22" />
            <stop offset="1" stopColor="#8b6fe8" stopOpacity="0.02" />
          </linearGradient>
          <linearGradient id="ember" x1="0" y1="1" x2="1" y2="0">
            <stop stopColor="#ff9159" stopOpacity="0.9" />
            <stop offset="0.6" stopColor="#c07ede" stopOpacity="0.4" />
            <stop offset="1" stopColor="#8b6fe8" stopOpacity="0.05" />
          </linearGradient>
        </defs>
        <Painting />
      </svg>

      {/* The seam, removed at both ends: the scene has to emerge from the
          panel rather than start at a line across it. */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-x-0 top-0 -z-10 h-20 bg-gradient-to-b from-rail to-transparent"
      />
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-x-0 bottom-0 -z-10 h-32 bg-gradient-to-t from-rail via-rail/80 to-transparent"
      />

      <p className="absolute right-5 bottom-9 left-5 font-serif text-[21px] leading-[1.14] text-fg">
        {caption}
      </p>
      <span aria-hidden="true" className="absolute bottom-5 left-5 h-px w-8 bg-fg/30" />
    </div>
  );
}
