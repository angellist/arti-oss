package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/go-chi/chi/v5"
	chimid "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/admin"
	"github.com/angellist/arti-oss/internal/apikeys"
	"github.com/angellist/arti-oss/internal/apps"
	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/comments"
	"github.com/angellist/arti-oss/internal/config"
	"github.com/angellist/arti-oss/internal/embed"
	"github.com/angellist/arti-oss/internal/groups"
	"github.com/angellist/arti-oss/internal/llm"
	"github.com/angellist/arti-oss/internal/mcp"
	"github.com/angellist/arti-oss/internal/obo"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/roles"
	"github.com/angellist/arti-oss/internal/slacknotify"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/opensearch"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// ServeCmd is the `arti-server serve` subcommand. All configuration comes
// from internal/config (env vars, plus an optional ARTI_CONFIG_FILE YAML)
// so the binary is k8s-friendly; see that package for the full surface and
// precedence rules.
type ServeCmd struct{}

func (*ServeCmd) Run(_ *kong.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	// Route package-level slog calls (slog.Info etc., e.g. internal/apikeys)
	// through the same JSON handler. Without this they fall back to Go's
	// plain-text default, which Datadog can't parse — every such line gets
	// its status guessed from the stream (stderr ⇒ error, even for INFO).
	slog.SetDefault(logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load(config.RequireDatabase, config.RequireStorage)
	if err != nil {
		return err
	}

	// Fail fast on an unsafe JWT signing key: a deployed env that booted with an
	// empty / dev-default / too-short key would sign tokens with a guessable
	// secret, letting anyone forge admin tokens. Local dev (ARTI_AUTH_DISABLED=
	// true) is exempt — it trusts no tokens.
	if err := auth.ValidateSigningKey(cfg.Auth.SigningKey, cfg.Auth.Disabled); err != nil {
		return fmt.Errorf("refusing to start: %w (set JWT_SIGNING_KEY to a strong secret, or run local dev with ARTI_AUTH_DISABLED=true)", err)
	}
	// ARTI_OBO_ENC_KEY derives from JWT_SIGNING_KEY when empty, so it inherits the
	// strength validated above; nudge ops to provision a dedicated value in prod.
	if !cfg.Auth.Disabled && cfg.Auth.OBO.EncKey == "" {
		logger.Warn("ARTI_OBO_ENC_KEY is empty; OBO envelope encryption is deriving its key from JWT_SIGNING_KEY — provision a dedicated value in prod")
	}

	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}

	s3, err := blob.NewS3(blob.S3Config{
		Endpoint:  trimScheme(cfg.Storage.Endpoint),
		AccessKey: cfg.Storage.AccessKey,
		SecretKey: cfg.Storage.SecretKey,
		Bucket:    cfg.Storage.Bucket,
		Region:    cfg.Storage.Region,
		UseSSL:    cfg.Storage.UseSSL,
	})
	if err != nil {
		return fmt.Errorf("init s3: %w", err)
	}

	pgstoreInst := pgstore.New(pool, s3, pgstore.Config{
		IdPGroupsMaxAge: cfg.Auth.IdPGroupsMaxAge.Std(),
	})
	stopInvalidationBus := pgstoreInst.StartInvalidationBus(ctx)
	defer stopInvalidationBus()

	// OpenSearch — full-text + vector search. nil when OPENSEARCH_ENDPOINT is empty.
	osClient := opensearch.New(opensearch.Config{
		Endpoint: cfg.Search.Endpoint,
		Username: cfg.Search.Username,
		Password: cfg.Search.Password,
		Region:   cfg.Search.Region,
	}, logger)
	osIndexer := opensearch.NewIndexer(osClient, pgstoreInst, logger)
	if osClient != nil {
		if err := osClient.EnsureIndex(ctx); err != nil {
			logger.Warn("opensearch: index setup failed (search will use Postgres fallback)", "err", err)
			osClient = nil
			osIndexer = nil
		} else {
			logger.Info("opensearch: connected", "endpoint", cfg.Search.Endpoint)
		}
	}

	svc := artifacts.NewService(pgstoreInst, cfg.Server.BaseURL, osClient, osIndexer)
	signer := auth.NewJWTSigner([]byte(cfg.Auth.SigningKey))
	pairs := auth.NewInMemPairStore()

	// Email allowlist used by every auth path (Dex verifier, HS256
	// test-mode tokens, and the /auth/test issuer). Entries are bare
	// domains or full addresses. Lives in process state so callers don't
	// have to thread it through.
	auth.SetAllowedEmails(cfg.Auth.AllowedEmails)
	logger.Info("email allowlist", "entries", auth.AllowedEmails())

	auth.SetAdminEmails(cfg.Admin.Emails)
	logger.Info("admin allowlist", "emails", auth.AdminEmails())

	// RBAC bootstrap: re-assert the ADMIN role assignment for each configured
	// admin email (idempotent). Config stays the lockout-proof floor — the
	// env var is always re-applied on boot — while additional assignments
	// (to other people or to groups) live in the DB and persist.
	if err := pgstoreInst.EnsureAdminAssignments(ctx, cfg.Admin.Emails); err != nil {
		return fmt.Errorf("seed ADMIN role assignments: %w", err)
	}

	// Advertise the protected-resource metadata as an ABSOLUTE URL in the
	// WWW-Authenticate header (RFC 9728); MCP/OAuth clients (e.g. Runlayer)
	// reject a relative path as an unsafe upstream URL.
	auth.SetMetadataBaseURL(cfg.Server.BaseURL)

	root := chi.NewRouter()
	root.Use(chimid.RequestID)
	root.Use(chimid.RealIP)
	root.Use(reqLogger(logger))
	root.Use(chimid.Recoverer)
	root.Use(chimid.Timeout(60 * time.Second))
	root.Use(securityHeaders)

	// Public routes ─────────────────────────────────────────────────
	// HEAD too: the Docker image's healthcheck probes with `wget --spider`
	// (a HEAD request), which chi would otherwise 405 — leaving the
	// container permanently unhealthy.
	healthz := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }
	root.Get("/healthz", healthz)
	root.Head("/healthz", healthz)
	root.Get("/openapi.yaml", openapiHandler)

	root.Mount("/.well-known", auth.WellKnownRoutes(auth.MetadataConfig{
		BaseURL:   cfg.Server.BaseURL,
		IssuerURL: cfg.Auth.IssuerURL,
	}))

	// CLI pair endpoints are public — single-use exchange/refresh.
	root.Post("/auth/cli/exchange", auth.CLIExchangeHandler(signer, pairs, 7*24*time.Hour, 90*24*time.Hour))
	root.Post("/auth/login/confirm", auth.ConfirmLoginHandler(signer, pairs, pgstoreInst, cfg.Server.CookieSecure))
	root.Post("/auth/cli/refresh", auth.CLIRefreshHandler(signer, 7*24*time.Hour, 90*24*time.Hour))

	// Device Authorization Grant (RFC 8628) — headless agents obtain a
	// human-approved, upload-scoped token. Public, server-to-server.
	devCfg := auth.DeviceConfig{
		Store:         pgstoreInst,
		Signer:        signer,
		BaseURL:       cfg.Server.BaseURL,
		AccessTTL:     cfg.Auth.Device.TokenTTL.Std(),
		MaxRefreshTTL: cfg.Auth.Device.TokenMaxTTL.Std(),
	}
	// Rate-limited per client IP: public + unauthenticated + one DB INSERT each.
	root.With(ipRateLimiter(cfg.Auth.Device.CodeRPM)).Post("/auth/device/code", auth.DeviceCodeHandler(devCfg))
	root.Get("/auth/device", auth.DeviceConfirmHandler(devCfg))
	root.Post("/auth/device/token", auth.DeviceTokenHandler(devCfg))
	root.Post("/auth/device/refresh", auth.DeviceRefreshHandler(devCfg))

	// MCP OAuth (RFC 7591 dynamic client registration + RFC 6749
	// authorization-code grant). Lets Runlayer and other MCP gateways
	// register themselves and broker user logins through arti without
	// pre-shared credentials.
	mcpOAuth := auth.MCPOAuthConfig{
		BaseURL:    cfg.Server.BaseURL,
		Store:      pgstoreInst,
		Signer:     signer,
		AccessTTL:  7 * 24 * time.Hour,
		RefreshTTL: 90 * 24 * time.Hour,
	}
	// Rate-limited per client IP: public dynamic-client-registration, one DB INSERT each.
	root.With(ipRateLimiter(cfg.Auth.OAuthRegisterRPM)).Post("/oauth/register", auth.MCPRegisterHandler(mcpOAuth))
	root.Get("/oauth/authorize", auth.MCPAuthorizeHandler(mcpOAuth))
	root.Post("/oauth/token", auth.MCPTokenHandler(mcpOAuth))
	// RFC 7591 registration is POST-only, but an OAuth client's client-registration
	// probe (Runlayer's deep diagnostic) GETs the discovered registration_endpoint
	// first. Answer a non-POST method with a clean 405 so that probe doesn't fall
	// through to the SSO-redirecting FE catch-all and loop into "too many
	// redirects". Mirrors the /mcp/* guard below; chi routes HEAD→GET so this
	// covers HEAD too. The token endpoint is likewise POST-only but isn't part of
	// the discovery probe surface, so it's left as-is.
	root.Get("/oauth/register", methodNotAllowed(http.MethodPost))

	// Build the OIDC verifier (Dex) once at startup. nil if unconfigured.
	oidcVerifier, err := auth.NewOIDCVerifier(ctx, auth.OIDCConfig{
		IssuerURL:      cfg.Auth.IssuerURL,
		Audience:       cfg.Auth.Audience,
		RequiredGroups: cfg.Auth.RequiredGroups,
	})
	if err != nil {
		return fmt.Errorf("init OIDC verifier: %w", err)
	}
	if oidcVerifier != nil {
		logger.Info("OIDC auth enabled", "issuer", cfg.Auth.IssuerURL,
			"audience", cfg.Auth.Audience, "required_groups", strings.Join(cfg.Auth.RequiredGroups, ","))
	} else {
		logger.Warn("OIDC auth disabled (AUTH_DEX_ISSUER_URL unset)")
	}

	// Test mode: mint HS256 tokens without going through Dex.
	if cfg.Auth.TestMode {
		logger.Warn("TEST MODE: /auth/test mints tokens without Dex — must be off in prod")
		root.Method(http.MethodPost, "/auth/test", auth.TestRoutes(signer))
	}

	// /auth/logout: clear the arti_session cookie and bounce to the
	// configured logout URL — typically the SSO proxy's sign-out endpoint
	// (ARTI_LOGOUT_URL / auth.logout_url) so the upstream IdP session is
	// also revoked. Unset → return to the app base URL.
	logoutTarget := cfg.Auth.LogoutURL
	if logoutTarget == "" {
		logoutTarget = cfg.Server.BaseURL + "/"
	}
	root.Get("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1,
			Secure: cfg.Server.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, logoutTarget, http.StatusFound)
	})

	// Interactive login. /auth/login is the mode-agnostic entry point the
	// web FE, CLI, and device-confirm page use; auth.mode picks what backs
	// it (see internal/config):
	//
	//   oidc   arti runs the authorization-code flow itself (PKCE + nonce)
	//          against the configured issuer; /auth/callback completes it.
	//   proxy  (and the legacy "" default) trust the X-Auth-Request-*
	//          headers a fronting proxy injects after verifying the user.
	//
	// Both paths finish identically (device approval / CLI pairing /
	// arti_session cookie). /auth/google/login remains as a legacy alias.
	switch cfg.Auth.Mode {
	case "oidc":
		login, lerr := auth.NewOIDCLogin(ctx, auth.OIDCLoginConfig{
			IssuerURL:      cfg.Auth.IssuerURL,
			ClientID:       cfg.Auth.ClientID,
			ClientSecret:   cfg.Auth.ClientSecret,
			Scopes:         cfg.Auth.Scopes,
			GroupsClaim:    cfg.Auth.GroupsClaim,
			BaseURL:        cfg.Server.BaseURL,
			RequiredGroups: cfg.Auth.RequiredGroups,
			Signer:         signer,
			Pairs:          pairs,
			DeviceStore:    pgstoreInst,
			Capturer:       pgstoreInst,
			AccessTTL:      7 * 24 * time.Hour,
			CookieSecure:   cfg.Server.CookieSecure,
			StateKey:       []byte(cfg.Auth.SigningKey),
		})
		if lerr != nil {
			return fmt.Errorf("init oidc login: %w", lerr)
		}
		root.Get("/auth/login", login.LoginHandler())
		root.Get("/auth/callback", login.CallbackHandler())
		root.Get("/auth/google/login", func(w http.ResponseWriter, r *http.Request) {
			target := "/auth/login"
			if q := r.URL.RawQuery; q != "" {
				target += "?" + q
			}
			http.Redirect(w, r, target, http.StatusFound)
		})
		logger.Info("interactive login: built-in oidc", "issuer", cfg.Auth.IssuerURL, "client_id", cfg.Auth.ClientID)
	default: // "" (legacy) or "proxy"
		ingress := auth.IngressLoginHandler(cfg.Auth.RequiredGroups, signer, pairs, pgstoreInst, 7*24*time.Hour, cfg.Server.CookieSecure, pgstoreInst)
		root.Get("/auth/login", ingress)
		root.Get("/auth/google/login", ingress)
		// /auth/callback exists only in oidc mode, but the public ingress
		// routes it here in every mode. Answer 404 explicitly — like the
		// /mcp/* guard — so the path can't fall through to the FE reverse
		// proxy (whose /auth rewrite would bounce it around, not 404).
		root.Get("/auth/callback", http.NotFound)
	}

	// Authed routes (REST + MCP) ─────────────────────────────────────
	var authMiddleware func(http.Handler) http.Handler
	authCfg := auth.NewConfig(oidcVerifier, signer, cfg.Auth.ServiceSecret, cfg.Auth.RequiredGroups)
	authCfg.ServiceSecretEmail = cfg.Auth.ServiceEmail
	authCfg.APIKeys = apikeys.NewAuthenticator(pgstoreInst)
	if cfg.Auth.Disabled {
		logger.Warn("AUTH DISABLED: every request is attributed to "+cfg.Auth.LocalEmail+" — must be off in prod",
			"local_email", cfg.Auth.LocalEmail)
		authMiddleware = auth.Disabled(cfg.Auth.LocalEmail)
	} else {
		authMiddleware = auth.RequireAuth(authCfg)
	}
	commentsSvc := comments.NewService(pool, pgstoreInst, signer)
	// Slack DM notifications on comment events (nil when no bot token → off).
	if notifier := slacknotify.New(cfg.Notifications.SlackBotToken, logger); notifier != nil {
		commentsSvc.SetNotifier(notifier, cfg.Server.BaseURL)
		logger.Info("slack comment notifications enabled")
	}
	// Inject the comments overlay into served HTML pages, authed by a scoped
	// token minted here.
	svc.SetEmbedTokenFn(commentsSvc.SignEmbedToken)

	// Embed comment API: token-authed + CORS, for the overlay injected into
	// sandboxed served-HTML pages. Public group (its own token auth).
	commentsSvc.MountEmbed(root)

	// Apps proxy: the governed MCP back end for APP artifacts. The only
	// built-in servers are deployment-neutral and in-process: arti-self
	// (credential-free local verification, reachable when auth is disabled)
	// and llm (the single-Claude completion, gated by each app's
	// arti-app.json allowlist). Every remote upstream — MCP gateways,
	// OBO-brokered connectors — comes from ARTI_APP_MCP_SERVERS, a JSON map
	// name->{resource_url,auth,scope} merged over these built-ins.
	// Mounted on the public group (its own injected-token auth + CORS),
	// like the comments embed. The OAuth TokenProvider is wired below once
	// available; until then auth'd servers return 501.
	appServers := map[string]apps.ServerConfig{
		"arti-self": {Name: "arti-self", ResourceURL: "http://127.0.0.1" + cfg.Server.Addr + "/mcp", Auth: "none"},
		"llm":       {Name: "llm", Auth: "service"},
	}
	if cfg.Apps.MCPServersJSON != "" {
		var extra map[string]apps.ServerConfig
		if err := json.Unmarshal([]byte(cfg.Apps.MCPServersJSON), &extra); err != nil {
			return fmt.Errorf("bad ARTI_APP_MCP_SERVERS: %w", err)
		}
		for k, v := range extra {
			v.Name = k
			appServers[k] = v
		}
	}
	// OBO OAuth broker — arti as an OAuth *client* (the user→arti→upstream OBO
	// leg). Discovers the resource's AS (RFC 9728 → 8414), DCR-registers a shared
	// client, runs PKCE authorize, and brokers a per-user token; its callback is
	// a public route. State is a self-contained encrypted token and the client +
	// tokens live in Postgres, so it's correct across pods. The callback base
	// defaults to the browser-facing ARTI_BASE_URL (correct in every deployed env
	// and for local dev); set ARTI_OBO_CALLBACK_BASE only to override it.
	callbackBase := cfg.Auth.OBO.CallbackBase
	if callbackBase == "" {
		callbackBase = cfg.Server.BaseURL
	}
	// identifyCaller resolves the arti-authenticated browser identity at the
	// Runlayer callback so the OBO consent is bound to the user who started it.
	// The callback is on the public ingress, where X-Auth-Request-Email is
	// client-settable (forgeable) — so we trust ONLY the cryptographically
	// signed arti_session cookie, which the browser sends on the top-level
	// callback navigation and cannot forge. Empty (local dev, no cookie) → "".
	identifyCaller := func(r *http.Request) string {
		ck, err := r.Cookie(auth.CookieName)
		if err != nil {
			return ""
		}
		claims, verr := signer.Verify(ck.Value)
		if verr != nil {
			return ""
		}
		return claims.Email
	}
	// Envelope-encryption key for the OBO broker: a dedicated ARTI_OBO_ENC_KEY if
	// set, else derived from the app's JWT signing key. HKDF inside NewCipher
	// domain-separates the state-token key from the at-rest column key.
	oboMaster := []byte(cfg.Auth.OBO.EncKey)
	if len(oboMaster) == 0 {
		oboMaster = []byte(cfg.Auth.SigningKey)
	}
	oboCipher, err := obo.NewCipher(oboMaster)
	if err != nil {
		return fmt.Errorf("init OBO cipher: %w", err)
	}
	oboBroker := obo.New(callbackBase, identifyCaller, obo.NewPGStore(pool, oboCipher), oboCipher)
	root.Get("/oauth/obo/callback", oboBroker.Callback)

	// Built-in llm.complete completion service (Auth:"service"). Service key from
	// env; usage ledger + budgets in Postgres. nil completer ⇒ the `llm` server
	// returns 501.
	var completer apps.Completer
	if cfg.LLM.APIKey != "" {
		llmSvc, lerr := llm.NewService(llm.Config{
			APIKey:       cfg.LLM.APIKey,
			DefaultModel: cfg.LLM.DefaultModel,
			Allowed:      cfg.LLM.AllowedModels,
			Store: llm.NewPGStore(pool, llm.Budgets{
				RPMPerViewer: cfg.LLM.RPMPerViewer,
				TPHPerViewer: cfg.LLM.TPHPerViewer,
				TPHPerApp:    cfg.LLM.TPHPerApp,
				TPDPerOrg:    cfg.LLM.TPDPerOrg,
			}),
		})
		if lerr != nil {
			return fmt.Errorf("init llm service: %w", lerr)
		}
		completer = llmSvc
		// Same service powers the upload modal's "auto-fill metadata"
		// (haiku names/slugs/labels an upload from its content).
		svc.SetMetadataCompleter(llmSvc)
		logger.Info("llm.complete enabled", "default_model", cfg.LLM.DefaultModel)
	} else {
		logger.Warn("llm.complete disabled (ANTHROPIC_API_KEY unset)")
	}

	// One MCP server instance, shared by the /mcp route and the apps proxy's
	// in-process arti-read short-circuit (so an APP can read other artifacts as
	// the viewer with no OBO consent popup; see apps.Service.SetArtiReader).
	mcpSrv := mcp.NewServer(svc, commentsSvc)
	appsSvc := apps.New(pgstoreInst, signer, appServers, oboBroker, completer, logger)
	appsSvc.SetArtiReader(mcpSrv)
	svc.SetAppTokenFn(appsSvc.SignAppToken)
	svc.SetAppTokenVerifyFn(appsSvc.VerifyEmbedToken)
	svc.SetEmbedFilesTokenFn(appsSvc.SignEmbedFilesToken)
	svc.SetAppFrameAncestors(cfg.Apps.FrameAncestors)
	appsSvc.MountProxy(root)
	// Public, token-scoped sibling-file route for rendered PACKAGE/APP pages —
	// see filesBaseFor's doc comment. Mounted outside authMiddleware like the
	// apps proxy above; it verifies its own token instead of a cookie/bearer.
	svc.MountFileToken(root)

	// Embed surfaces: serve arti artifacts into external (cross-site) iframes,
	// authenticated by each surface's shared secret (no SSO — public group, like
	// the comments embed + apps proxy above). svc satisfies embed.ArtServer;
	// appsSvc.VerifyEmbedToken authorizes the sibling-files path. No-op when
	// ARTI_EMBED_SURFACES is unset; a malformed surface fails startup.
	surfaces, serr := embed.ParseSurfaces(cfg.Embedding.SurfacesJSON)
	if serr != nil {
		return serr
	}
	if len(surfaces) > 0 {
		embedSvc := embed.New(surfaces, svc, appsSvc)
		embedSvc.Mount(root)
		// Per-user popup handshake for user-mode surfaces (no-op without one).
		// Identity comes ONLY from the signed arti_session cookie (identifyCaller
		// — the mint route sits behind the protected ingress in prod, but header
		// trust is deliberately not used so the handler is safe under any
		// routing); with auth disabled, local dev attributes to the local email.
		identifyEmbed := identifyCaller
		if cfg.Auth.Disabled {
			identifyEmbed = func(*http.Request) string { return cfg.Auth.LocalEmail }
		}
		embedSvc.MountMint(root, embed.MintConfig{
			Identify:     identifyEmbed,
			Minter:       appsSvc,
			ChallengeKey: []byte(cfg.Auth.SigningKey),
			Store:        embed.NewPGPendingTokenStore(pool),
			CookieSecure: cfg.Server.CookieSecure,
			Logger:       logger,
		})
		names := make([]string, 0, len(surfaces))
		for n := range surfaces {
			names = append(names, n)
		}
		logger.Info("embed surfaces enabled", "surfaces", names)
	}

	root.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Use(auth.EnforceUploadScope(pgstoreInst, int64(cfg.Auth.Device.MaxUploadBytes)))
		r.Post("/auth/device/revoke", auth.DeviceRevokeHandler(devCfg))
		artifacts.Mount(r, svc)
		commentsSvc.Mount(r)
		admin.NewService(pool, pgstoreInst).Mount(r)
		groups.NewService(pgstoreInst).Mount(r)
		roles.NewService(pgstoreInst).Mount(r)
		apikeys.NewService(pgstoreInst, cfg.Auth.APIKeys.MaxTTL.Std(), func(ctx context.Context, email string) (bool, error) {
			return pgstoreInst.HasPermission(ctx, email, rbac.ManageAPIKeys)
		}).Mount(r, ipRateLimiter(cfg.Auth.APIKeys.MintRPM))
		r.Handle("/mcp", mcpSrv.Handler())
	})
	root.Group(func(r chi.Router) {
		if cfg.Auth.Disabled {
			r.Use(auth.Disabled(cfg.Auth.LocalEmail))
		} else {
			r.Use(auth.RequireAuthOrRedirect(authCfg, "/auth/login"))
		}
		r.Use(auth.EnforceUploadScope(pgstoreInst, int64(cfg.Auth.Device.MaxUploadBytes)))
		artifacts.MountApp(r, svc)
	})

	// Nothing lives UNDER the MCP endpoint — it is the exact path /mcp above.
	// Return a clean 404 for /mcp/* instead of letting it fall through to the FE
	// reverse proxy below, whose middleware redirects unauthenticated browser
	// navigations into the SSO login chain. An MCP/OAuth client probing the
	// OpenID-Connect-Discovery path-prefix form (/mcp/.well-known/openid-configuration)
	// would otherwise follow that chain into "too many redirects". Spec-compliant
	// discovery uses the RFC 9728 §3.1 path-scoped metadata under /.well-known
	// (see auth.WellKnownRoutes), not a path under /mcp.
	root.Handle("/mcp/*", http.NotFoundHandler())

	// FE reverse proxy (everything else).
	if cfg.Server.WebURL != "" {
		target, err := url.Parse(cfg.Server.WebURL)
		if err != nil {
			return fmt.Errorf("bad ARTI_WEB_URL: %w", err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		// Catalog/viewer pages (served by the Next.js FE) carry their own
		// `frame-ancestors` CSP (next.config.ts) that allows trusted internal
		// origins to iframe the viewer — e.g. couch's artifact side panel
		// embedding /s/<slug>?v=full. The global securityHeaders middleware set
		// `X-Frame-Options: SAMEORIGIN`, which can't express an allowlist and
		// would still block that cross-subdomain framing, so drop it here and
		// let frame-ancestors be authoritative. Mirrors the APP/embed paths,
		// which delete XFO for the same reason. API/MCP/auth responses are not
		// proxied through here and keep XFO.
		root.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Del("X-Frame-Options")
			proxy.ServeHTTP(w, r)
		}))
	}

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown on SIGTERM/SIGINT.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info("shutting down")
		shutdownCtx, c2 := context.WithTimeout(context.Background(), 10*time.Second)
		defer c2()
		_ = srv.Shutdown(shutdownCtx)
	}()

	logger.Info("arti-server listening",
		"addr", cfg.Server.Addr, "base", cfg.Server.BaseURL, "bucket", cfg.Storage.Bucket, "web", cfg.Server.WebURL, "test_mode", cfg.Auth.TestMode)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func reqLogger(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// kube-probe polls /healthz every few seconds per pod; logging each
			// hit was ~2/3 of arti's entire log volume. Skip the access log for
			// the probe only — every real route stays logged.
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			ww := chimid.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			l.Info("http",
				"m", r.Method, "p", redactLogPath(r.URL.Path),
				"s", ww.Status(), "b", ww.BytesWritten(),
				"d", time.Since(start).Milliseconds())
		})
	}
}

// redactLogPath masks scoped app tokens embedded in sibling-file URL paths
// before logging, so a bearer credential never lands in centralized HTTP
// logs: /embed/{surface}/_files/{token}/... (embed sibling-file route) and
// /api/artifacts/{id}/files-token/{token}/... (in-catalog sibling-file
// route, see filesBaseFor). Other paths are returned unchanged.
func redactLogPath(p string) string {
	for _, marker := range [...]string{"/_files/", "/files-token/"} {
		i := strings.Index(p, marker)
		if i < 0 {
			continue
		}
		rest := p[i+len(marker):]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return p[:i+len(marker)] + "<redacted>"
		}
		return p[:i+len(marker)] + "<redacted>" + rest[j:]
	}
	return p
}

func trimScheme(s string) string {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return s
}

// methodNotAllowed answers with 405 and an Allow header, giving a wrong-method
// probe of an API endpoint a clean, non-redirecting response instead of letting
// it fall through to the FE reverse proxy (which redirects an unauthenticated
// request into the SSO login chain).
func methodNotAllowed(allowed ...string) http.HandlerFunc {
	allow := strings.Join(allowed, ", ")
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func openapiHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapiYAML)
}

// securityHeaders attaches defense-in-depth response headers to every
// response served by arti-server (REST, MCP, and the Next.js proxy).
// Per-response CSPs (the sandbox header attached to uploaded HTML
// bodies in internal/artifacts) layer on top.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Prevent browsers from guessing a Content-Type when one is
		// supplied — closes the "text/plain body sniffed as HTML"
		// gap where uploaded content could be reinterpreted.
		h.Set("X-Content-Type-Options", "nosniff")
		// Don't leak full URLs in Referer when users follow links
		// out to external sites (artifact pages can carry slug /
		// scope / query in the URL).
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// arti may iframe its own content (PACKAGE viewer), so we
		// allow SAMEORIGIN framing but block third-party framing.
		h.Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}
