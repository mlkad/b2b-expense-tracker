import type { ReactElement } from "react";

/**
 * Marks for the vendors a finance team sees constantly.
 *
 * Drawn, not fetched. A favicon service means guessing a domain from a free-text
 * merchant string, handing a third party the list of every vendor a tenant has
 * ever claimed against, and rendering a broken square for the corner shop that
 * has no website - on a product whose whole argument is that the finance team's
 * data stays theirs.
 *
 * Simplified deliberately. At twenty pixels a logo is a silhouette and a colour;
 * the detail that makes it legal-department-correct at poster size is noise
 * here, and tracing it would be the wrong kind of accurate.
 */
export interface Brand {
  /** The plate behind the mark. */
  background: string;
  mark: ReactElement;
}

const g = (children: ReactElement) => children;

export const BRAND_MARKS: Record<string, Brand> = {
  google: {
    background: "#ffffff",
    mark: g(
      <g>
        <path d="M23.4 12.3c0-.8-.1-1.6-.2-2.3H12v4.5h6.4a5.5 5.5 0 0 1-2.4 3.6v3h3.9c2.3-2.1 3.5-5.2 3.5-8.8Z" fill="#4285f4" />
        <path d="M12 24c3.2 0 5.9-1.1 7.9-2.9l-3.9-3c-1 .7-2.3 1.1-4 1.1-3.1 0-5.7-2.1-6.6-4.9H1.4v3.1A12 12 0 0 0 12 24Z" fill="#34a853" />
        <path d="M5.4 14.3a7.2 7.2 0 0 1 0-4.6V6.6H1.4a12 12 0 0 0 0 10.8l4-3.1Z" fill="#fbbc05" />
        <path d="M12 4.8c1.8 0 3.4.6 4.6 1.8l3.5-3.5A12 12 0 0 0 1.4 6.6l4 3.1C6.3 6.9 8.9 4.8 12 4.8Z" fill="#ea4335" />
      </g>,
    ),
  },
  figma: {
    background: "#1b1c23",
    mark: g(
      <g>
        <path d="M8.5 24a3.75 3.75 0 0 0 3.75-3.75V16.5H8.5a3.75 3.75 0 0 0 0 7.5Z" fill="#0acf83" />
        <path d="M4.75 12.25A3.75 3.75 0 0 1 8.5 8.5h3.75v7.5H8.5a3.75 3.75 0 0 1-3.75-3.75Z" fill="#a259ff" />
        <path d="M4.75 4.75A3.75 3.75 0 0 1 8.5 1h3.75v7.5H8.5a3.75 3.75 0 0 1-3.75-3.75Z" fill="#f24e1e" />
        <path d="M12.25 1H16a3.75 3.75 0 0 1 0 7.5h-3.75V1Z" fill="#ff7262" />
        <path d="M19.75 12.25A3.75 3.75 0 1 1 16 8.5a3.75 3.75 0 0 1 3.75 3.75Z" fill="#1abcfe" />
      </g>,
    ),
  },
  aws: {
    background: "#232f3e",
    mark: g(
      <g>
        <text
          x="12"
          y="12"
          textAnchor="middle"
          fontFamily="Inter, sans-serif"
          fontSize="8"
          fontWeight="700"
          fill="#ffffff"
        >
          aws
        </text>
        <path
          d="M4 16.5c4.6 2.6 11.4 2.6 16 0"
          stroke="#ff9900"
          strokeWidth="2"
          strokeLinecap="round"
          fill="none"
        />
      </g>,
    ),
  },
  uber: {
    background: "#000000",
    mark: g(
      <text
        x="12"
        y="15.5"
        textAnchor="middle"
        fontFamily="Inter, sans-serif"
        fontSize="8.5"
        fontWeight="700"
        fill="#ffffff"
      >
        Uber
      </text>,
    ),
  },
  wework: {
    background: "#000000",
    mark: g(
      <text
        x="12"
        y="15.5"
        textAnchor="middle"
        fontFamily="Inter, sans-serif"
        fontSize="9"
        fontWeight="700"
        fill="#ffffff"
      >
        we
      </text>,
    ),
  },
  slack: {
    background: "#ffffff",
    mark: g(
      <g>
        <path d="M6 15a2.5 2.5 0 1 1-2.5-2.5H6V15Zm1.25 0a2.5 2.5 0 0 1 5 0v6.5a2.5 2.5 0 0 1-5 0V15Z" fill="#e01e5a" />
        <path d="M9.75 6a2.5 2.5 0 1 1 2.5-2.5V6h-2.5Zm0 1.25a2.5 2.5 0 0 1 0 5H3.5a2.5 2.5 0 0 1 0-5h6.25Z" fill="#36c5f0" />
        <path d="M18 9.75a2.5 2.5 0 1 1 2.5 2.5H18v-2.5Zm-1.25 0a2.5 2.5 0 0 1-5 0V3.5a2.5 2.5 0 0 1 5 0v6.25Z" fill="#2eb67d" />
        <path d="M14.25 18a2.5 2.5 0 1 1-2.5 2.5V18h2.5Zm0-1.25a2.5 2.5 0 0 1 0-5h6.25a2.5 2.5 0 0 1 0 5h-6.25Z" fill="#ecb22e" />
      </g>,
    ),
  },
  notion: {
    background: "#ffffff",
    mark: g(
      <text
        x="12"
        y="17"
        textAnchor="middle"
        fontFamily="Georgia, serif"
        fontSize="15"
        fontWeight="700"
        fill="#000000"
      >
        N
      </text>,
    ),
  },
  github: {
    background: "#ffffff",
    mark: g(
      <path
        d="M12 1.5a10.5 10.5 0 0 0-3.3 20.5c.5.1.7-.2.7-.5v-2c-2.9.6-3.5-1.2-3.5-1.2-.5-1.2-1.2-1.5-1.2-1.5-.9-.7.1-.6.1-.6 1 .1 1.6 1 1.6 1 .9 1.6 2.4 1.1 3 .9.1-.7.4-1.1.7-1.4-2.3-.3-4.8-1.2-4.8-5.2 0-1.2.4-2.1 1.1-2.9-.1-.3-.5-1.3.1-2.8 0 0 .9-.3 2.9 1.1a10 10 0 0 1 5.2 0c2-1.4 2.9-1.1 2.9-1.1.6 1.5.2 2.5.1 2.8.7.8 1.1 1.7 1.1 2.9 0 4-2.5 4.9-4.8 5.2.4.3.7 1 .7 2v3c0 .3.2.6.7.5A10.5 10.5 0 0 0 12 1.5Z"
        fill="#181717"
      />,
    ),
  },
  stripe: {
    background: "#635bff",
    mark: g(
      <text
        x="12"
        y="17"
        textAnchor="middle"
        fontFamily="Inter, sans-serif"
        fontSize="14"
        fontWeight="700"
        fill="#ffffff"
      >
        S
      </text>,
    ),
  },
};

/** The first mark whose name appears in the merchant string. */
export function brandMarkFor(name: string): Brand | undefined {
  const words = name.toLowerCase().replace(/[^a-z0-9]+/g, " ").split(" ").filter(Boolean);
  for (const word of words) {
    if (BRAND_MARKS[word]) return BRAND_MARKS[word];
  }
  return undefined;
}
