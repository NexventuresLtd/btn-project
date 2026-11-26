import type { Lang } from "@/i18n";

/**
 * Small inline SVG flags for the language switcher. Inline (not emoji) because
 * flag emoji do not render on Linux/Chrome. 20×14 rounded, decorative.
 *  en → United Kingdom · am → Ethiopia · rw → Rwanda · fr → France
 */
export function Flag({ lang, className = "size-5" }: { lang: Lang; className?: string }) {
  return (
    <span className={`inline-block overflow-hidden rounded-[3px] shadow-[0_0_0_1px_rgb(0_0_0/0.08)] ${className}`} aria-hidden="true">
      <svg viewBox="0 0 20 14" className="block h-full w-full" preserveAspectRatio="xMidYMid slice">
        {lang === "en" ? <UK /> : lang === "am" ? <Ethiopia /> : lang === "fr" ? <France /> : <Rwanda />}
      </svg>
    </span>
  );
}

function UK() {
  return (
    <>
      <rect width="20" height="14" fill="#012169" />
      <path d="M0 0l20 14M20 0L0 14" stroke="#fff" strokeWidth="2.8" />
      <path d="M0 0l20 14M20 0L0 14" stroke="#C8102E" strokeWidth="1.1" />
      <path d="M10 0v14M0 7h20" stroke="#fff" strokeWidth="4" />
      <path d="M10 0v14M0 7h20" stroke="#C8102E" strokeWidth="2.2" />
    </>
  );
}

function Ethiopia() {
  return (
    <>
      <rect width="20" height="14" fill="#078930" />
      <rect width="20" height="9.33" y="0" fill="#078930" />
      <rect width="20" height="4.67" y="4.67" fill="#FCDD09" />
      <rect width="20" height="4.67" y="9.33" fill="#DA121A" />
      <circle cx="10" cy="7" r="3.1" fill="#0F47AF" />
      <path
        d="M10 4.5l.62 1.9h2l-1.62 1.18.62 1.9L10 8.3l-1.62 1.18.62-1.9L7.38 6.4h2z"
        fill="#FCDD09"
        stroke="#FCDD09"
        strokeWidth=".3"
      />
    </>
  );
}

function Rwanda() {
  return (
    <>
      <rect width="20" height="14" fill="#20603D" />
      <rect width="20" height="7" fill="#00A1DE" />
      <rect width="20" height="3.5" y="7" fill="#FAD201" />
      <g transform="translate(15 3.2)">
        <circle r="1.7" fill="#E5BE01" />
        {Array.from({ length: 24 }).map((_, i) => (
          <rect key={i} x="-0.12" y="-2.9" width="0.24" height="1.1" fill="#E5BE01" transform={`rotate(${i * 15})`} />
        ))}
      </g>
    </>
  );
}

function France() {
  return (
    <>
      <rect width="20" height="14" fill="#FFFFFF" />
      <rect width="6.67" height="14" fill="#002395" />
      <rect width="6.67" height="14" x="13.33" fill="#ED2939" />
    </>
  );
}
