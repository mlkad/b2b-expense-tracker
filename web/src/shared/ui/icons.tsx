import type { SVGProps } from "react";

/**
 * The icon set, inline.
 *
 * Inline rather than an icon package: this is a dashboard with about a dozen
 * glyphs in it, and pulling in a library to get them costs more bytes than the
 * whole set and hands a third party a place in the render path. They are all
 * one grid, one stroke weight, and inherit currentColor - which is what makes
 * an icon follow the text beside it into a hover or a disabled state.
 *
 * Every icon here is decorative: it sits beside a label that already says the
 * same thing, so it is hidden from the accessible tree. An icon that is the
 * only label needs a title, and there are none of those.
 */
type IconProps = SVGProps<SVGSVGElement>;

function Icon({ children, ...rest }: IconProps) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.6}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      {...rest}
    >
      {children}
    </svg>
  );
}

export function OverviewIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <rect x="3.5" y="3.5" width="17" height="17" rx="4.5" />
      <path d="m8.5 12.2 2.4 2.4 4.6-5" />
    </Icon>
  );
}

export function ExpensesIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <rect x="2.75" y="5" width="18.5" height="14" rx="3" />
      <path d="M2.75 9.5h18.5M6.5 14.5h3.5" />
    </Icon>
  );
}

export function ApprovalsIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M6 3.5h12a1.5 1.5 0 0 1 1.5 1.5v3A5.5 5.5 0 0 1 14 13.5h-4A5.5 5.5 0 0 1 4.5 8V5A1.5 1.5 0 0 1 6 3.5Z" />
      <path d="M12 13.5v3M8 20.5h8" />
    </Icon>
  );
}

export function BudgetsIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M4.2 9h15.6l-1.1 9.3a2 2 0 0 1-2 1.7H7.3a2 2 0 0 1-2-1.7Z" />
      <path d="M8.75 9V6.75a3.25 3.25 0 0 1 6.5 0V9" />
    </Icon>
  );
}

export function OrganisationIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="12" cy="8" r="3.75" />
      <path d="M4.75 20.25a7.25 7.25 0 0 1 14.5 0" />
    </Icon>
  );
}

export function SearchIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <circle cx="11" cy="11" r="6.5" />
      <path d="m16 16 4 4" />
    </Icon>
  );
}

export function BellIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M18 9a6 6 0 1 0-12 0c0 4.2-1.5 5.6-2 6.2-.3.4 0 1.05.5 1.05h15c.5 0 .8-.65.5-1.05-.5-.6-2-2-2-6.2Z" />
      <path d="M10 19.5a2.2 2.2 0 0 0 4 0" />
    </Icon>
  );
}

export function ChevronDownIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="m6.5 9.5 5.5 5.5 5.5-5.5" />
    </Icon>
  );
}

export function PlusIcon(props: IconProps) {
  return (
    <Icon {...props} strokeWidth={2}>
      <path d="M12 5.5v13M5.5 12h13" />
    </Icon>
  );
}

export function MoreIcon(props: IconProps) {
  return (
    <Icon {...props} strokeWidth={2.2}>
      <path d="M6 12h.01M12 12h.01M18 12h.01" />
    </Icon>
  );
}

export function ArrowUpIcon(props: IconProps) {
  return (
    <Icon {...props}>
      <path d="M12 19V5m0 0-5.5 5.5M12 5l5.5 5.5" />
    </Icon>
  );
}

/** The mark. A cut gem: facets, because the product is about scrutiny. */
export function LogoMark(props: IconProps) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" {...props}>
      <path
        d="M12 2.2 21.2 8v8L12 21.8 2.8 16V8Z"
        fill="url(#logo-gem)"
        stroke="currentColor"
        strokeOpacity="0.45"
        strokeWidth="1.1"
        strokeLinejoin="round"
      />
      <path
        d="M12 2.2 2.8 8l9.2 4.8L21.2 8Zm0 10.6v9"
        stroke="currentColor"
        strokeOpacity="0.35"
        strokeWidth="1.1"
        strokeLinejoin="round"
        fill="none"
      />
      <defs>
        <linearGradient id="logo-gem" x1="4" y1="3" x2="20" y2="21" gradientUnits="userSpaceOnUse">
          <stop stopColor="#cdbcfa" />
          <stop offset="0.55" stopColor="#8b6fe8" />
          <stop offset="1" stopColor="#4b3a85" />
        </linearGradient>
      </defs>
    </svg>
  );
}
