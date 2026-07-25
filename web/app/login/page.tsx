import Image from "next/image";

export const dynamic = "force-dynamic";

type SP = Record<string, string | string[] | undefined>;

export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<SP>;
}) {
  const sp = await searchParams;
  const returnTo =
    (Array.isArray(sp.return_to) ? sp.return_to[0] : sp.return_to) ?? "/";
  const href = `/auth/login?return_to=${encodeURIComponent(returnTo)}`;
  return (
    <main className="flex min-h-screen items-center justify-center bg-neutral-50">
      <div className="flex flex-col items-center gap-6 rounded-xl border border-neutral-200 bg-white p-12 shadow-sm">
        <Image
          src="/logo.png"
          alt="arti"
          width={96}
          height={96}
          className="rounded-xl"
          priority
        />
        <h1 className="text-4xl font-bold tracking-tight text-neutral-900">
          arti
        </h1>
        <p className="max-w-xs text-center text-sm text-neutral-500">
          Sign in with your <span className="font-medium text-neutral-700">organization</span> account
          to browse artifacts.
        </p>
        <a
          href={href}
          className="rounded-md bg-blue-600 px-6 py-2.5 text-sm font-medium text-white shadow-sm transition hover:bg-blue-700"
        >
          Continue with SSO
        </a>
      </div>
    </main>
  );
}
