/**
 * The tile beside a merchant, a department or a person.
 *
 * Vendors people actually claim against are recognised by sight before they are
 * read, and a column of identical grey squares throws that away. So the tiles
 * carry brand colour: a small table of the vendors this product sees constantly,
 * and a hash of the name for everything else.
 *
 * Not a fetched favicon. That means guessing a domain from a merchant string,
 * handing a third party the list of every merchant a tenant has ever claimed
 * against, and rendering a broken square for the corner shop with no website -
 * on a page whose whole point is that the finance team's data stays theirs.
 */
const BRANDS: Record<string, string> = {
  adobe: "#eb1000",
  airbnb: "#ff5a5f",
  amazon: "#ff9900",
  apple: "#e8e8ed",
  atlassian: "#2684ff",
  aws: "#ff9900",
  booking: "#1668e3",
  canva: "#00c4cc",
  cloudflare: "#f6821f",
  datadog: "#7b42bc",
  deliveroo: "#00ccbc",
  dell: "#0076ce",
  digitalocean: "#0080ff",
  dropbox: "#0061ff",
  figma: "#f24e1e",
  github: "#e6e6e6",
  gitlab: "#fc6d26",
  google: "#4285f4",
  heroku: "#79589f",
  hilton: "#2f6bbd",
  hubspot: "#ff7a59",
  intercom: "#1f8ded",
  jira: "#2684ff",
  linear: "#5e6ad2",
  lyft: "#ff00bf",
  mailchimp: "#ffe01b",
  microsoft: "#f25022",
  miro: "#ffd02f",
  notion: "#e6e6e6",
  openai: "#10a37f",
  oreilly: "#d3002d",
  pret: "#a4243b",
  salesforce: "#00a1e0",
  sentry: "#e1567c",
  slack: "#611f69",
  starbucks: "#00704a",
  stripe: "#635bff",
  trainline: "#00a88f",
  twilio: "#f22f46",
  uber: "#c9c9c9",
  vercel: "#e6e6e6",
  wework: "#f5a623",
  zoom: "#2d8cff",
};

/** The first brand whose name appears in the merchant string. */
function brandFor(name: string): string | undefined {
  const words = name.toLowerCase().replace(/[^a-z0-9]+/g, " ").split(" ").filter(Boolean);
  for (const word of words) {
    if (BRANDS[word]) return BRANDS[word];
  }
  return undefined;
}

/**
 * A stable hue for anything not in the table, so a merchant keeps the same
 * colour across every screen and every session. Saturation and lightness are
 * fixed, which is what stops the hash producing something unreadable.
 */
function hueOf(name: string): number {
  let hash = 0;
  for (let i = 0; i < name.length; i += 1) hash = (hash * 31 + name.charCodeAt(i)) | 0;
  return Math.abs(hash) % 360;
}

/** Whether black or white sits better on a colour, by perceived luminance. */
function inkOn(hex: string): string {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
  const lift = (c: number) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  const luminance = 0.2126 * lift(r) + 0.7152 * lift(g) + 0.0722 * lift(b);
  return luminance > 0.45 ? "#15121f" : "#ffffff";
}

export function Monogram({ name, className = "" }: { name: string; className?: string }) {
  const brand = brandFor(name);
  const letter = name.trim().charAt(0).toUpperCase() || "?";

  const style = brand
    ? { backgroundColor: brand, color: inkOn(brand) }
    : {
        backgroundColor: `hsl(${hueOf(name.toLowerCase())} 44% 26%)`,
        color: `hsl(${hueOf(name.toLowerCase())} 74% 76%)`,
      };

  return (
    <span
      // Decorative: the name is spelled out immediately beside it.
      aria-hidden="true"
      className={`grid size-5 shrink-0 place-items-center rounded text-[10px] font-bold ${className}`}
      style={style}
    >
      {letter}
    </span>
  );
}
