/**
 * The tile beside a merchant's name.
 *
 * Not a favicon. Turning "Pret A Manger" into a logo means guessing a domain
 * and asking a third party for it, which hands every merchant a user has ever
 * claimed for to whoever runs that service - and renders a broken square for
 * the corner shop that has no website. A letter on a colour derived from the
 * name is available offline, correct for every merchant, and does the one job
 * this actually needs: giving the eye something to lock onto when scanning a
 * column of names.
 *
 * The hue is a hash, so a merchant keeps the same colour across every screen
 * and every session. Saturation and lightness are fixed, which is what stops
 * the hash from occasionally producing something unreadable.
 */
function hueOf(name: string): number {
  let hash = 0;
  for (let i = 0; i < name.length; i += 1) {
    hash = (hash * 31 + name.charCodeAt(i)) | 0;
  }
  return Math.abs(hash) % 360;
}

export function Monogram({ name, className = "" }: { name: string; className?: string }) {
  const hue = hueOf(name.toLowerCase());
  const letter = name.trim().charAt(0).toUpperCase() || "?";

  return (
    <span
      // Decorative: the merchant's name is spelled out immediately beside it.
      aria-hidden="true"
      className={`grid size-6 shrink-0 place-items-center rounded-md text-[11px] font-semibold ${className}`}
      style={{
        backgroundColor: `hsl(${hue} 42% 24%)`,
        color: `hsl(${hue} 72% 74%)`,
      }}
    >
      {letter}
    </span>
  );
}
