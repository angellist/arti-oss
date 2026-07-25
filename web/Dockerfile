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

FROM node:20-alpine AS web
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build /app/.next ./.next
COPY --from=build /app/public ./public
COPY --from=build /app/package.json ./package.json
COPY --from=build /app/node_modules ./node_modules
EXPOSE 3030
ENV PORT=3030
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:3030/login || exit 1
CMD ["npm", "run", "start"]
