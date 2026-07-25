import { HelpRailProvider } from "@/lib/rail-context";
import { helpNav } from "@/lib/help-docs";

export default function HelpLayout({ children }: { children: React.ReactNode }) {
  return (
    <HelpRailProvider nav={helpNav()}>
      <main className="flex-1 overflow-y-auto overflow-x-hidden bg-white">{children}</main>
    </HelpRailProvider>
  );
}
