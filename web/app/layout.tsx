import type { Metadata } from "next";
import { Geist, Petrona } from "next/font/google";
import "./globals.css";
import SideNav from "@/components/SideNav";
import { RailModeShell } from "@/lib/rail-context";
import { UploadProvider } from "@/lib/upload-context";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

// Mono now comes from the OS via `ui-monospace` in globals.css — no
// Google-Fonts fetch needed. Boxes-align with the user's editor.

// Petrona — low-contrast serif used only for the wordmark.
const petrona = Petrona({
  variable: "--font-petrona",
  subsets: ["latin"],
  weight: ["500", "600", "700"],
});

export const metadata: Metadata = {
  title: { default: "arti", template: "%s — arti" },
  description: "AngelList artifact catalog",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      className={`${geistSans.variable} ${petrona.variable} h-full antialiased`}
    >
      <body className="min-h-full bg-neutral-50">
        <RailModeShell>
          <UploadProvider>
            <div className="flex min-h-screen">
              <SideNav />
              {/* pt-12 below md leaves room for the fixed mobile top bar
                  that SideNav renders; desktop has no top bar so pt-0. */}
              <div className="flex min-w-0 flex-1 flex-col pt-12 md:pt-0">{children}</div>
            </div>
          </UploadProvider>
        </RailModeShell>
      </body>
    </html>
  );
}
