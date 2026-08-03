"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";
import type { HelpNav } from "@/lib/help-docs";

export default function SideNavHelp({ nav }: { nav: HelpNav }) {
  const pathname = usePathname();
  return (
    <nav className="space-y-5">
      <Link href="/" className="block text-[12px] text-neutral-500 hover:text-neutral-800">← Back to catalog</Link>
      {nav.map((section) => (
        <div key={section.dir}>
          <div className="mb-1 text-[10px] font-medium uppercase tracking-wide text-neutral-400">{section.label}</div>
          <ul className="space-y-0.5">
            {section.docs.map((d) => {
              const href = `/help/${d.slug}`;
              const active = pathname === href;
              return (
                <li key={d.slug}>
                  <Link
                    href={href}
                    className={`block rounded px-2 py-1 text-[13px] ${active ? "bg-neutral-100 font-medium text-neutral-900" : "text-neutral-600 hover:bg-neutral-50"}`}
                  >
                    {d.title}
                  </Link>
                </li>
              );
            })}
          </ul>
        </div>
      ))}
    </nav>
  );
}
