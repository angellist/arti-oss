// Shared look for the collapsed rail's icon tiles: a 36×34 hit target centred
// in the strip, with its label in a tooltip to the right on hover or focus.
export function railTileClass(opts: { active?: boolean; accent?: boolean } = {}): string {
  const base =
    "group relative grid h-[34px] w-9 place-items-center rounded-[7px] transition focus-visible:outline-none ";
  if (opts.accent) {
    return base + "bg-[#eef5f3] text-[#2a8a8a] hover:bg-[#bcd8d4] hover:text-[#1f6868] focus-visible:bg-[#bcd8d4]";
  }
  if (opts.active) return base + "bg-neutral-200 text-neutral-900";
  return base + "text-neutral-500 hover:bg-neutral-100 hover:text-neutral-900 focus-visible:bg-neutral-100 focus-visible:text-neutral-900";
}

export function RailTip({ children }: { children: React.ReactNode }) {
  return (
    <span
      aria-hidden="true"
      className="pointer-events-none absolute left-[calc(100%+10px)] top-1/2 z-50 hidden -translate-y-1/2 whitespace-nowrap rounded-[5px] bg-neutral-900 px-2 py-1 text-[12px] font-medium leading-[1.3] text-white group-hover:block group-focus-visible:block"
    >
      <span className="absolute -left-1 top-1/2 -translate-y-1/2 border-4 border-l-0 border-transparent border-r-neutral-900" />
      {children}
    </span>
  );
}
