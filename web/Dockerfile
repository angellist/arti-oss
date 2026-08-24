# ==============================================================================
# arti-web — Next.js sidecar
# ==============================================================================
FROM node:20-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:20-alpine AS build
WORKDIR /app
# NEXT_PUBLIC_* values are inlined at build time. The auth-disabled flag lets
# the local-evaluation compose profile build a UI that skips the login
# redirect; leave it unset (the default) for any real deployment.
ARG NEXT_PUBLIC_ARTI_AUTH_DISABLED=""
ENV NEXT_PUBLIC_ARTI_AUTH_DISABLED=$NEXT_PUBLIC_ARTI_AUTH_DISABLED
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN npm run build

# Pre-built image for the CI web-quality job. Mirrors the Go `test` target in
# the root Dockerfile: it carries the source plus devDependencies (eslint,
# typescript, vitest) so `npm run lint`, `npm run typecheck` and `npm test` run
# with no further installs and no network at step time. It reuses the `build`
# stage, so the deps/build layers are shared with the web image.
FROM build AS web_test
WORKDIR /app
CMD ["npm", "test"]

FROM node:20-alpine AS web
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build /app/.next ./.next
COPY --from=build /app/public ./public
COPY --from=build /app/package.json ./package.json
COPY --from=build /app/node_modules ./node_modules
EXPOSE 3030
ENV PORT=3030
# `npm run start` prints npm's own "New major version of npm available!" block
# on stderr a few seconds after boot. It is plain text, so Datadog assigns its
# status from the stream and every line becomes an ERROR event: 300 of the 564
# ERROR events arti produced in a week, none of them a fault, four per pod
# start. The notifier's registry check is async, which is why only a long-lived
# process like `next start` lives long enough to print it.
ENV NPM_CONFIG_UPDATE_NOTIFIER=false
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:3030/login || exit 1
CMD ["npm", "run", "start"]
