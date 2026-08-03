import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  {
    // These three arrived as errors with eslint-config-next 16 (the React
    // Compiler rule set) and found 16 pre-existing violations, because eslint
    // had never run in CI — so nobody saw them. Each needs a real refactor in
    // the app shell and the viewer, not a mechanical edit:
    //
    //   set-state-in-effect  13× — two distinct shapes. Reset-on-prop-change
    //                        (BrowseTable, SideNav's drawer, the viewer's
    //                        per-view modes) wants React's during-render
    //                        adjustment or a `key`. localStorage hydration
    //                        (ViewerToolbar, SideNav width/collapse) wants
    //                        useSyncExternalStore with a getServerSnapshot —
    //                        NOT a lazy useState initializer, which would
    //                        reintroduce an SSR hydration mismatch.
    //   refs                  2× — SideNav writes widthRef/modeKindRef during
    //                        render; the write belongs in an effect.
    //   immutability          1× — ArtifactViewer assigns to `info.title`
    //                        (a prop) and leans on router.refresh().
    //
    // Downgraded to warn so the CI gate can go green on day one and start
    // catching *new* problems immediately. `npm run lint` pins
    // --max-warnings at today's count, so this number can only fall.
    // Do not raise it: fix a warning, then lower the pin.
    rules: {
      "react-hooks/set-state-in-effect": "warn",
      "react-hooks/refs": "warn",
      "react-hooks/immutability": "warn",
    },
  },
  // Override default ignores of eslint-config-next.
  globalIgnores([
    // Default ignores of eslint-config-next:
    ".next/**",
    "out/**",
    "build/**",
    "next-env.d.ts",
    // Static assets, never source. Notably public/comments-embed.js is the
    // minified esbuild bundle that `npm run build:embed` emits — gitignored,
    // so it is absent from a fresh checkout but present after a build (e.g.
    // in the CI web_test image). Linting it reported 149 warnings from
    // minified output. eslint's flat config does not read .gitignore.
    "public/**",
  ]),
]);

export default eslintConfig;
