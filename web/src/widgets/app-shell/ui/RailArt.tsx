import type { ReactElement } from "react";
/**
 * The artwork at the foot of the sidebar.
 *
 * Drawn as inline SVG rather than shipped as images. Four full-bleed
 * photographs would be most of this application's transfer size, they would
 * need a decode on every navigation, and they would have to be art-directed for
 * three breakpoints. A few polygons over a gradient cost about a kilobyte,
 * scale to any panel, and follow the palette when it changes.
 *
 * It is decoration, so it is hidden from the accessible tree - and the line of
 * text over it is a mood, not information. Nothing here is the only place
 * anything is said.
 */
export type Scene = "ridge" | "arch" | "road" | "crescent";

/** A lit ridge under a violet sky: the expense list. */
function Ridge() {
  return (
    <>
      <rect width="240" height="300" fill="url(#sky-violet)" />
      <circle cx="168" cy="74" r="46" fill="url(#glow-soft)" />
      <path d="M0 196 74 108l44 52 30-34 92 78v96H0Z" fill="#1a1130" />
      <path d="M0 196 74 108l30 36-46 156H0Z" fill="#2b1c4d" />
      <path d="m74 108 44 52-30 132H58Z" fill="#3d2a69" />
      <path d="M0 232 62 168l50 58 34-38 94 74v38H0Z" fill="#120c22" />
    </>
  );
}

/** An arch with light coming through it: the approvals queue. */
function Arch() {
  return (
    <>
      <rect width="240" height="300" fill="url(#sky-deep)" />
      <path d="M96 300V150a24 24 0 0 1 48 0v150Z" fill="url(#shaft)" />
      <path
        d="M78 300V148a42 42 0 0 1 84 0v152h-18V148a24 24 0 0 0-48 0v152Z"
        fill="#2a1c4a"
      />
      <path d="M0 258 56 210l46 32 40-26 98 60v42H0Z" fill="#150e28" />
    </>
  );
}

/** A road at night, seen from above: the budgets. */
function Road() {
  return (
    <>
      <rect width="240" height="300" fill="url(#sky-warm)" />
      <circle cx="150" cy="112" r="62" fill="url(#glow-soft)" />
      {/* The same curve three times: a wide soft pass for the bloom, the lit
          ribbon over it, and a hairline for the edge the light catches. */}
      <path d="M-14 306c46-64 108-64 132-114s-16-78 26-116" stroke="url(#ember)" strokeWidth="26" strokeOpacity="0.28" strokeLinecap="round" fill="none" />
      <path d="M-14 306c46-64 108-64 132-114s-16-78 26-116" stroke="url(#ember)" strokeWidth="9" strokeLinecap="round" fill="none" />
      <path d="M-14 306c46-64 108-64 132-114s-16-78 26-116" stroke="#ffd9c2" strokeWidth="2" strokeOpacity="0.75" strokeLinecap="round" fill="none" />
      <path d="M0 252 68 206l46 26 44-32 82 50v50H0Z" fill="#150e28" />
      <path d="M0 282 74 244l50 22 46-18 70 30v22H0Z" fill="#0d0818" />
    </>
  );
}

/** A crescent over a dark horizon: the organisation, and the default. */
function Crescent() {
  return (
    <>
      <rect width="240" height="300" fill="url(#sky-cool)" />
      <circle cx="164" cy="88" r="52" fill="url(#glow-soft)" />
      <circle cx="164" cy="88" r="34" fill="#efeaff" fillOpacity="0.92" />
      <circle cx="148" cy="78" r="34" fill="url(#sky-cool)" />
      <path d="M0 226 52 186l38 30 52-46 98 70v60H0Z" fill="#1b1236" />
      <path d="M0 262 60 224l52 34 40-22 88 40v24H0Z" fill="#100a1e" />
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
    <div className="relative isolate mt-auto min-h-72 overflow-hidden">
      <svg
        aria-hidden="true"
        viewBox="0 0 240 300"
        preserveAspectRatio="xMidYMax slice"
        className="absolute inset-0 -z-10 size-full"
      >
        <defs>
          <linearGradient id="sky-violet" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#241748" />
            <stop offset="0.55" stopColor="#180f30" />
            <stop offset="1" stopColor="#0a0810" />
          </linearGradient>
          <linearGradient id="sky-deep" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#1b1038" />
            <stop offset="0.6" stopColor="#160e2c" />
            <stop offset="1" stopColor="#0a0810" />
          </linearGradient>
          <linearGradient id="sky-warm" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#1d1233" />
            <stop offset="0.6" stopColor="#150e26" />
            <stop offset="1" stopColor="#0a0810" />
          </linearGradient>
          <linearGradient id="sky-cool" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#1e1440" />
            <stop offset="0.6" stopColor="#150e2a" />
            <stop offset="1" stopColor="#0a0810" />
          </linearGradient>
          <radialGradient id="glow-soft">
            <stop stopColor="#cdbcfa" stopOpacity="0.55" />
            <stop offset="1" stopColor="#cdbcfa" stopOpacity="0" />
          </radialGradient>
          <linearGradient id="shaft" x1="0" y1="0" x2="0" y2="1">
            <stop stopColor="#efe6ff" stopOpacity="0.85" />
            <stop offset="1" stopColor="#8b6fe8" stopOpacity="0.05" />
          </linearGradient>
          <linearGradient id="ember" x1="0" y1="1" x2="1" y2="0">
            <stop stopColor="#ff9a63" stopOpacity="0.9" />
            <stop offset="1" stopColor="#8b6fe8" stopOpacity="0.15" />
          </linearGradient>
        </defs>
        <Painting />
      </svg>

      {/* The caption is legible by construction: every scene ends in the canvas
          colour at its foot, and this sits in that band. */}
      <p className="absolute right-6 bottom-10 left-6 font-serif text-[23px] leading-[1.12] text-fg">
        {caption}
      </p>
      <span aria-hidden="true" className="absolute bottom-6 left-6 h-px w-9 bg-fg/35" />
    </div>
  );
}
