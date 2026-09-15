import type { Metadata } from "next";
import ThemePicker from "@/components/ThemePicker";

export const metadata: Metadata = { title: "appearance" };

// Appearance is a per-browser choice held in localStorage — no account, no
// server call, so the page is static and visible to everyone.
export default function AppearanceSettingsPage() {
  return (
    <div className="px-6 pt-4">
      <h2 className="text-[14px] font-semibold text-neutral-900">Appearance</h2>
      <p className="mt-0.5 mb-3 max-w-2xl text-[12px] text-neutral-500">
        applies as you click, to this browser only. system follows your OS between light and
        dark. artifacts that carry their own styling — HTML pages, apps, PDFs — render as
        their author wrote them.
      </p>
      <ThemePicker />
    </div>
  );
}
