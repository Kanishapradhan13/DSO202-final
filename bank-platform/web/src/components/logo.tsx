export function Logo({ light = false }: { light?: boolean }) {
  return (
    <span className="inline-flex items-center gap-2 font-semibold tracking-tight">
      <svg viewBox="0 0 32 32" className="h-7 w-7" aria-hidden>
        <rect width="32" height="32" rx="8" fill="#0f4c81" />
        <path d="M8 13 16 8l8 5v1H8z" fill="#fbbf24" />
        <path d="M10 15.5h2.4v7H10zm4.8 0h2.4v7h-2.4zm4.8 0H22v7h-2.4zM8 23.5h16V25H8z" fill="#fff" />
      </svg>
      <span className={light ? "text-white" : ""}>
        CBMP <span className={light ? "text-saffron-400" : "text-brand-600"}>Bank</span>
      </span>
    </span>
  );
}
