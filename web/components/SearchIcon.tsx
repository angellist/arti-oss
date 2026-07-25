// A non-emoji magnifier that inherits text color (stroke = currentColor), so it
// can sit inline in front of the rail's SEARCH item and inside the search box
// and match the neighbouring text weight/color. Shared by SideNavSearch and
// CatalogTable.
export function SearchIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
    >
      <circle cx="11" cy="11" r="7" />
      <line x1="21" y1="21" x2="16.65" y2="16.65" />
    </svg>
  );
}
