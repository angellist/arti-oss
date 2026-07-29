import { SearchRailProvider } from "@/lib/rail-context";
import NewDiagram from "@/components/NewDiagram";

export const dynamic = "force-dynamic";

export const metadata = { title: "New diagram" };

export default function NewDiagramPage() {
  return (
    <SearchRailProvider>
      <main className="min-h-screen bg-white pb-12">
        <NewDiagram />
      </main>
    </SearchRailProvider>
  );
}
