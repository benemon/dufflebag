package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/benemon/dufflebag/internal/audit"
	"github.com/benemon/dufflebag/internal/bagdrop"
	"github.com/benemon/dufflebag/internal/compat/hcp2023"
	"github.com/benemon/dufflebag/internal/compat/hcpauth"
	"github.com/benemon/dufflebag/internal/compat/rm2019"
	"github.com/benemon/dufflebag/internal/credseal"
	"github.com/benemon/dufflebag/internal/domain/identity"
	"github.com/benemon/dufflebag/internal/keyring"
	platform "github.com/benemon/dufflebag/internal/platform/v1"
	"github.com/benemon/dufflebag/internal/scan"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/benemon/dufflebag/internal/webhook"
	"github.com/benemon/dufflebag/web"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	tokenTTL                    = 15 * time.Minute
	defaultShutdownGracePeriod  = 10 * time.Second
	objectStorageStartupTimeout = 5 * time.Second
	// 32 bytes is the HMAC-SHA256 block size; shorter keys weaken the signature
	// without any warning that they have.
	minSigningKeyBytes = 32
	bagDropAuthBase    = "https://auth.idp.hashicorp.com"
	bagDropAPIBase     = "https://api.cloud.hashicorp.com"
)

var (
	version string
	commit  string
)

func main() {
	// The migrate subcommand exists so schema changes can run under a
	// privileged role in an init container or pre-deploy step, letting the
	// serving process run as a role that cannot alter schema. The server still
	// migrates on start for single-role setups; on a prepared database that is
	// a no-op.
	if len(os.Args) > 1 {
		if os.Args[1] != "migrate" || len(os.Args) > 2 {
			log.Fatalf("unknown command %q — the only subcommand is migrate", strings.Join(os.Args[1:], " "))
		}
		if err := runMigrations(); err != nil {
			log.Fatalf("migrate: %v", err)
		}
		log.Print("migrations applied")
		return
	}
	databaseURL := os.Getenv("DFBG_DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DFBG_DATABASE_URL is required")
	}
	address := os.Getenv("DFBG_HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	trustedProxies, err := trustedProxiesFromEnvironment()
	if err != nil {
		log.Fatal(err)
	}
	// No default: a signing key that ships with the binary is the same as no
	// signing key, since anyone holding it can mint a token for any principal.
	//
	// On encrypted deployments the signing and audit keys live in the wrapped
	// keyring instead of the environment (ADR-0024): fewer raw secrets in the
	// process env, one rotation mechanism. An env copy alongside the keyring
	// would be a second source of truth, so it is refused rather than ignored.
	signingKey := os.Getenv("DFBG_TOKEN_SIGNING_KEY")
	credentialKey, err := credseal.ResolveEnvironmentKey(
		os.Getenv(credseal.CredentialKeyEnv), os.Getenv(bagdrop.CredentialKeyEnv), bagdrop.CredentialKeyEnv,
	)
	if err != nil {
		log.Fatal(err)
	}
	if os.Getenv(keyring.ProviderEnv) != "" {
		for _, variable := range []string{
			"DFBG_TOKEN_SIGNING_KEY", "DFBG_AUDIT_HMAC_KEY", "DFBG_AUDIT_HMAC_KEY_VERSION",
			credseal.CredentialKeyEnv, bagdrop.CredentialKeyEnv,
		} {
			if os.Getenv(variable) != "" {
				log.Fatalf("%s must not be set when %s is configured: on an encrypted deployment this key lives in the wrapped keyring", variable, keyring.ProviderEnv)
			}
		}
	} else if len(signingKey) < minSigningKeyBytes {
		log.Fatalf("DFBG_TOKEN_SIGNING_KEY is required and must be at least %d bytes", minSigningKeyBytes)
	}
	issuerURL := os.Getenv("DFBG_TOKEN_ISSUER")
	if issuerURL == "" {
		issuerURL = "https://dufflebag.local"
	}
	maxRequestBodyBytes := int64(hcp2023.DefaultMaxRequestBodyBytes)
	if configured := os.Getenv("DFBG_API_MAX_BODY_BYTES"); configured != "" {
		parsed, err := strconv.ParseInt(configured, 10, 64)
		if err != nil || parsed <= 0 {
			log.Fatal("DFBG_API_MAX_BODY_BYTES must be a positive integer")
		}
		maxRequestBodyBytes = parsed
	}
	// TLS is not optional for a real client: hcp-sdk-go rejects any auth URL
	// whose scheme is not https (config/hcp.go), so a plaintext listener cannot
	// serve the token endpoint to the SDK at all. Both are required together —
	// half a pair produces a listener that starts and then cannot be used.
	certFile, keyFile := os.Getenv("DFBG_TLS_CERT_FILE"), os.Getenv("DFBG_TLS_KEY_FILE")
	if (certFile == "") != (keyFile == "") {
		log.Fatal("DFBG_TLS_CERT_FILE and DFBG_TLS_KEY_FILE must be set together")
	}
	shutdownGracePeriod, err := configuredShutdownGracePeriod()
	if err != nil {
		log.Fatal(err)
	}
	scannerConfig, err := scannerConfigurationFromEnvironment()
	if err != nil {
		log.Fatal(err)
	}
	bagDropReconcileInterval, err := configuredBagDropReconcileInterval()
	if err != nil {
		log.Fatal(err)
	}
	allowPrivateWebhooks, err := configuredWebhookAllowPrivate()
	if err != nil {
		log.Fatal(err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := store.Migrate(db); err != nil {
		log.Fatalf("migrate database: %v", err)
	}
	// Checked AFTER migrating, because migration legitimately needs privileges
	// the serving role should not have. Refusing to serve is the point: a role
	// that bypasses row-level security disables every tenancy boundary without
	// erroring, logging, or failing a test.
	if err := store.AssertRLSApplies(context.Background(), db); err != nil {
		log.Fatalf("refusing to serve: %v", err)
	}

	objects, err := objectStorageFromEnvironment(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if err := validateScannerObjectStorage(scannerConfig, objects); err != nil {
		log.Fatal(err)
	}
	repository := store.NewRepository(db)
	if objects != nil {
		repository = store.NewRepositoryWithObjectStore(db, objects)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	providerCtx, cancelProvider := context.WithCancel(context.Background())
	defer cancelProvider()
	// The one-way door and the sealed refusal both live here, before any
	// listener exists: a mismatched mode or an unreachable key service must
	// never serve a request (ADR-0024).
	ring, provider, err := initializeEncryption(providerCtx, repository, time.Now)
	if err != nil {
		log.Fatalf("encryption at rest: %v", err)
	}
	if ring != nil {
		// Arms payload encryption and row MACs. Without this line the whole
		// posture silently degrades to plaintext — which is why the runtime
		// integration lane asserts a hand-inserted root fails to sign in.
		repository.SetKeyring(ring)
	}
	var issuer *identity.BasicAuthIssuer
	if ring != nil {
		issuer, err = identity.NewKeyringAuthIssuer(issuerURL, ring.TokenSigningKeys, tokenTTL)
	} else {
		issuer, err = identity.NewBasicAuthIssuer(issuerURL, []byte(signingKey), tokenTTL)
	}
	if err != nil {
		log.Fatalf("token issuer: %v", err)
	}
	auditHMACKeyVersion, auditHMACKey := auditHMACConfiguration()
	if ring != nil {
		auditHMACKey, auditHMACKeyVersion = ring.AuditHMACKey()
	}
	broker, err := initializeAudit(
		context.Background(), repository, logger, auditHMACKeyVersion, auditHMACKey,
	)
	if err != nil {
		log.Fatalf("audit initialization: %v", err)
	}
	var scannerService *store.ScannerService
	if scannerConfig != nil {
		client, err := scannerHTTPClient(*scannerConfig)
		if err != nil {
			log.Fatalf("scanner configuration: %v", err)
		}
		adapter := scan.NewOSV(scannerConfig.endpoint, client, time.Now)
		scannerService, err = store.NewScannerService(repository, adapter, broker, store.ScannerServiceConfig{
			AdapterName: scannerConfig.adapter, Engine: scannerConfig.endpoint,
			Workers: scannerConfig.workers, PassTimeout: scannerConfig.passTimeout,
			RunRetention: scannerConfig.runRetention, Interval: scannerConfig.interval,
			Logger: logger,
		})
		if err != nil {
			log.Fatalf("scanner initialization: %v", err)
		}
	}
	auditKeySource := audit.StaticHMACKey(auditHMACKeyVersion, auditHMACKey)
	var encryptionService platform.EncryptionService
	var runtimeKeyring *keyring.Service
	if ring != nil {
		runtimeKeyring = keyring.NewService(provider, repository, ring, logger)
		encryptionService = runtimeKeyring
		auditKeySource = func() (string, []byte) {
			key, version := ring.AuditHMACKey()
			return version, key
		}
	}
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	if runtimeKeyring != nil {
		go runtimeKeyring.Run(heartbeatCtx)
	}
	scannerCtx, cancelScanner := context.WithCancel(context.Background())
	defer cancelScanner()
	if scannerService != nil {
		go scannerService.Run(scannerCtx)
	}

	// One process, two surfaces. In HCP these are separate hosts — auth at
	// HCP_AUTH_URL, the registry at HCP_API_ADDRESS — and keeping them on
	// distinct paths here means splitting them later is a deployment change
	// rather than a code change.
	token := hcpauth.NewHandler(repository, issuer, logger, trustedProxies...)
	packer := hcp2023.NewHandler(repository, issuer, logger, maxRequestBodyBytes)
	resourceManager := rm2019.NewHandler(repository, repository, issuer, logger)
	build := platform.BuildInfo{
		Version:     normalizedBuildValue(version, "dev"),
		Commit:      normalizedBuildValue(commit, "unknown"),
		APIVersions: mountedAPIVersions(rootRoutes(nil, nil, nil, nil)),
	}
	// A typed nil pointer would satisfy the interface and make every scanner
	// check look configured, so the nil stays untyped until a service exists.
	var platformScanner platform.Scanner
	if scannerService != nil {
		platformScanner = scannerService
	}
	bagDropSealer := bagdrop.NewCredentialSealer(ring, credentialKey)
	bagDropAdapters := bagdrop.Registry{
		bagdrop.AdapterHCPPacker: bagdrop.NewHCPPackerAdapter(bagDropAuthBase, bagDropAPIBase),
		bagdrop.AdapterDufflebag: bagdrop.NewDufflebagAdapterFactory(),
	}
	bagDropService := bagdrop.NewService(repository, bagDropSealer, bagDropAdapters)
	bagDropReconciler, err := bagdrop.NewReconciler(
		repository, bagDropSealer, bagDropAdapters, broker, bagDropReconcileInterval, logger,
	)
	if err != nil {
		log.Fatalf("Bag Drop reconciler initialization: %v", err)
	}
	bagDropRuntime := &bagdrop.Runtime{Service: bagDropService, Reconciler: bagDropReconciler}
	bagDropCtx, cancelBagDrop := context.WithCancel(context.Background())
	defer cancelBagDrop()
	bagDropDone := make(chan struct{})
	go func() {
		defer close(bagDropDone)
		bagDropReconciler.Run(bagDropCtx)
	}()
	<-bagDropReconciler.Started()
	credentialSealer := credseal.New(ring, credentialKey)
	webhookClient := webhook.NewHTTPClient(allowPrivateWebhooks, nil, nil)
	webhookService := webhook.NewService(repository, credentialSealer, webhookClient)
	webhookDispatcher, err := webhook.NewDispatcher(repository, credentialSealer, webhookClient, time.Second, time.Minute, logger)
	if err != nil {
		log.Fatalf("webhook dispatcher initialization: %v", err)
	}
	webhookCtx, cancelWebhooks := context.WithCancel(context.Background())
	defer cancelWebhooks()
	webhookDone := make(chan struct{})
	go func() {
		defer close(webhookDone)
		webhookDispatcher.Run(webhookCtx)
	}()
	<-webhookDispatcher.Started()
	platformPlane := platform.NewHandler(
		repository, repository, issuer, repository, logger, repository, broker,
		encryptionService, platformScanner, bagDropRuntime, webhookService, build,
	)
	applicationHandler := composeHandler(
		broker,
		auditKeySource,
		token,
		packer,
		resourceManager,
		platformPlane,
	)
	metricsRegistry := prometheus.NewRegistry()
	// Metrics are deliberately outside admission and audit. They see every
	// request while audit keeps ownership of its response capture and ordering.
	handler := instrumentServingHandler(applicationHandler, newHTTPMetrics(metricsRegistry))
	metricsServer := metricsServerFromEnvironment(metricsRegistry)

	// Whole-request timeouts, not just headers. Without them a client that opens
	// a connection and sends a byte at a time holds a goroutine, a connection
	// and — on the token endpoint — a verification permit indefinitely (duf-39p).
	// Generous enough for a slow link carrying a build's PATCH bodies.
	server := newHTTPServer(address, handler)
	shutdownSignal := make(chan os.Signal, 1)
	reopenSignal := make(chan os.Signal, 1)
	signal.Notify(shutdownSignal, os.Interrupt, syscall.SIGTERM)
	signal.Notify(reopenSignal, syscall.SIGHUP)
	defer signal.Stop(shutdownSignal)
	defer signal.Stop(reopenSignal)
	serveResult := make(chan error, 1)
	go func() {
		if certFile != "" {
			serveResult <- server.ListenAndServeTLS(certFile, keyFile)
			return
		}
		serveResult <- server.ListenAndServe()
	}()
	var metricsServeResult chan error
	if metricsServer != nil {
		metricsServeResult = make(chan error, 1)
		go func() { metricsServeResult <- metricsServer.ListenAndServe() }()
		log.Printf("metrics listening on %s (http, unauthenticated)", metricsServer.Addr)
	}

	if certFile != "" {
		log.Printf("listening on %s (https)", address)
	} else {
		// Plaintext remains available for tests and for a proxy that terminates TLS
		// upstream, but it cannot serve the SDK directly.
		log.Printf("listening on %s (http — the HCP SDK will refuse this for auth)", address)
	}

	for {
		select {
		case err := <-serveResult:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("serve: %v", err)
			}
			log.Fatal("server stopped without a shutdown signal")
		case err := <-metricsServeResult:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("metrics serve: %v", err)
			}
			log.Fatal("metrics server stopped without a shutdown signal")
		case <-reopenSignal:
			if err := broker.Reopen(); err != nil {
				logger.Warn("audit targets did not all reopen", "error", err)
			}
		case <-shutdownSignal:
			cancelProvider()
			cancelScanner()
			cancelBagDrop()
			cancelWebhooks()
			cancelHeartbeat()
			deadline := time.Now().Add(shutdownGracePeriod)
			select {
			case <-bagDropDone:
			case <-time.After(time.Until(deadline)):
				logger.Warn("Bag Drop reconciler did not stop before the shutdown deadline")
			}
			select {
			case <-webhookDone:
			case <-time.After(time.Until(deadline)):
				logger.Warn("webhook dispatcher did not stop before the shutdown deadline")
			}
			if err := shutdown(server, metricsServer, broker, deadline); err != nil {
				logger.Warn("shutdown did not fully drain", "error", err)
			}
			if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Warn("server stopped with an error during shutdown", "error", err)
			}
			if metricsServer != nil {
				if err := <-metricsServeResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Warn("metrics server stopped with an error during shutdown", "error", err)
				}
			}
			return
		}
	}
}

func trustedProxiesFromEnvironment() ([]netip.Prefix, error) {
	configured := os.Getenv("DFBG_TRUSTED_PROXIES")
	if configured == "" {
		return nil, nil
	}

	entries := strings.Split(configured, ",")
	proxies := make([]netip.Prefix, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			address, addressErr := netip.ParseAddr(entry)
			if addressErr != nil {
				return nil, fmt.Errorf("DFBG_TRUSTED_PROXIES entry %q must be an IP address or CIDR", entry)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		proxies = append(proxies, prefix.Masked())
	}
	return proxies, nil
}

type scannerRuntimeConfig struct {
	adapter        string
	endpoint       string
	format         string
	requestTimeout time.Duration
	passTimeout    time.Duration
	runRetention   time.Duration
	interval       time.Duration
	workers        int
	caFile         string
}

func scannerConfigurationFromEnvironment() (*scannerRuntimeConfig, error) {
	const prefix = "DFBG_SCANNER_"
	allowed := map[string]bool{
		"DFBG_SCANNER_ADAPTER": true, "DFBG_SCANNER_ENDPOINT": true,
		"DFBG_SCANNER_FORMAT": true, "DFBG_SCANNER_REQUEST_TIMEOUT": true,
		"DFBG_SCANNER_PASS_TIMEOUT": true, "DFBG_SCANNER_RUN_RETENTION": true,
		"DFBG_SCANNER_WORKERS": true, "DFBG_SCANNER_CA_FILE": true,
		"DFBG_SCANNER_INTERVAL": true,
	}
	adapter := os.Getenv("DFBG_SCANNER_ADAPTER")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, prefix) || name == "DFBG_SCANNER_ADAPTER" {
			continue
		}
		if adapter == "" {
			return nil, fmt.Errorf("%s is set but DFBG_SCANNER_ADAPTER is unset", name)
		}
		if !allowed[name] {
			return nil, fmt.Errorf("unknown scanner setting %s", name)
		}
	}
	if adapter == "" {
		return nil, nil
	}
	if adapter != "osv" {
		return nil, fmt.Errorf("unknown DFBG_SCANNER_ADAPTER %q", adapter)
	}
	config := &scannerRuntimeConfig{
		adapter: adapter, endpoint: "https://api.osv.dev", format: "purl",
		requestTimeout: 30 * time.Second, passTimeout: 15 * time.Minute,
		runRetention: 2160 * time.Hour, workers: 2, interval: 24 * time.Hour,
		caFile: os.Getenv("DFBG_SCANNER_CA_FILE"),
	}
	if configured := os.Getenv("DFBG_SCANNER_ENDPOINT"); configured != "" {
		config.endpoint = configured
	}
	if configured := os.Getenv("DFBG_SCANNER_FORMAT"); configured != "" {
		config.format = configured
	}
	if config.format != "purl" {
		return nil, fmt.Errorf("unknown DFBG_SCANNER_FORMAT %q", config.format)
	}
	var err error
	if config.requestTimeout, err = scannerDuration("DFBG_SCANNER_REQUEST_TIMEOUT", config.requestTimeout); err != nil {
		return nil, err
	}
	if config.passTimeout, err = scannerDuration("DFBG_SCANNER_PASS_TIMEOUT", config.passTimeout); err != nil {
		return nil, err
	}
	if config.interval, err = scannerDuration("DFBG_SCANNER_INTERVAL", config.interval); err != nil {
		return nil, err
	}
	if config.runRetention, err = scannerDuration("DFBG_SCANNER_RUN_RETENTION", config.runRetention); err != nil {
		return nil, err
	}
	if configured := os.Getenv("DFBG_SCANNER_WORKERS"); configured != "" {
		config.workers, err = strconv.Atoi(configured)
		if err != nil || config.workers <= 0 {
			return nil, errors.New("DFBG_SCANNER_WORKERS must be a positive integer")
		}
	}
	return config, nil
}

func scannerDuration(name string, fallback time.Duration) (time.Duration, error) {
	configured := os.Getenv(name)
	if configured == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(configured)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func configuredBagDropReconcileInterval() (time.Duration, error) {
	return scannerDuration("DFBG_BAGDROP_RECONCILE_INTERVAL", 5*time.Minute)
}

func configuredWebhookAllowPrivate() (bool, error) {
	configured := os.Getenv("DFBG_WEBHOOK_ALLOW_PRIVATE")
	if configured == "" {
		return false, nil
	}
	allow, err := strconv.ParseBool(configured)
	if err != nil {
		return false, errors.New("DFBG_WEBHOOK_ALLOW_PRIVATE must be true or false")
	}
	return allow, nil
}

func scannerHTTPClient(config scannerRuntimeConfig) (*http.Client, error) {
	client := &http.Client{Timeout: config.requestTimeout}
	if config.caFile == "" {
		return client, nil
	}
	pem, err := os.ReadFile(config.caFile)
	if err != nil {
		return nil, fmt.Errorf("read DFBG_SCANNER_CA_FILE: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system certificate pool: %w", err)
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("DFBG_SCANNER_CA_FILE contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	client.Transport = transport
	return client, nil
}

func validateScannerObjectStorage(config *scannerRuntimeConfig, objects *objectstore.Store) error {
	if config != nil && objects == nil {
		return errors.New("scanner enabled but object storage is not configured")
	}
	return nil
}

func runMigrations() error {
	databaseURL := os.Getenv("DFBG_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DFBG_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()
	return store.Migrate(db)
}

func objectStorageFromEnvironment(ctx context.Context) (*objectstore.Store, error) {
	config := objectstore.Config{
		Endpoint:  os.Getenv("DFBG_OBJECT_STORAGE_ENDPOINT"),
		Region:    os.Getenv("DFBG_OBJECT_STORAGE_REGION"),
		Bucket:    os.Getenv("DFBG_OBJECT_STORAGE_BUCKET"),
		AccessKey: os.Getenv("DFBG_OBJECT_STORAGE_ACCESS_KEY"),
		SecretKey: os.Getenv("DFBG_OBJECT_STORAGE_SECRET_KEY"),
	}
	if config == (objectstore.Config{}) {
		return nil, nil
	}
	objects, err := objectstore.New(config)
	if err != nil {
		return nil, fmt.Errorf("object storage configuration: %w", err)
	}
	checkCtx, cancel := context.WithTimeout(ctx, objectStorageStartupTimeout)
	defer cancel()
	if err := objects.CheckBucket(checkCtx); err != nil {
		return nil, fmt.Errorf("object storage availability: %w", err)
	}
	return objects, nil
}

type auditTargetLoader interface {
	ListAuditTargets(context.Context) ([]identity.AuditTarget, error)
}

// initializeEncryption enforces the ADR-0024 one-way door and, on encrypted
// deployments, produces the unwrapped keyring.
//
// The first boot stamps the configured mode — the presence or absence of a
// key provider in the environment — and every later boot compares against the
// stamp: the wrong state is unrepresentable rather than migrated. When the
// key service is unreachable the instance refuses to start (sealed, in
// Vault's vocabulary); already-running replicas are unaffected, because the
// keyring is never consulted per-write.
func initializeEncryption(
	ctx context.Context, repository *store.Repository, now func() time.Time,
) (*keyring.Keyring, keyring.Provider, error) {
	provider, err := keyring.ProviderFromEnvironment(ctx)
	if err != nil {
		return nil, nil, err
	}
	configured := provider != nil

	recorded, encrypted, err := repository.EncryptionMode(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !recorded {
		if err := repository.RecordEncryptionMode(ctx, configured, now()); err != nil {
			return nil, nil, err
		}
		// Re-read rather than trust the write: a racing first boot may have
		// stamped the other answer, and both replicas must agree with the row.
		if _, encrypted, err = repository.EncryptionMode(ctx); err != nil {
			return nil, nil, err
		}
	}
	switch {
	case encrypted && !configured:
		return nil, nil, fmt.Errorf("encryption mode mismatch: this instance has encryption at rest, but %s is not set — encryption is a one-way door chosen at first boot (ADR-0024); restore the key provider configuration", keyring.ProviderEnv)
	case !encrypted && configured:
		return nil, nil, fmt.Errorf("encryption mode mismatch: this instance was first booted without encryption at rest, and %s is now set — encryption is a one-way door chosen at first boot (ADR-0024); unset the key provider or start from a fresh database", keyring.ProviderEnv)
	case !configured:
		return nil, nil, nil
	}

	entries, err := repository.ListKeyringEntries(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) == 0 {
		ring, fresh, err := keyring.Generate(ctx, provider)
		if err != nil {
			return nil, nil, fmt.Errorf("sealed — cannot reach the key service to establish the keyring: %w", err)
		}
		stored, err := repository.CreateKeyringEntries(ctx, fresh, now())
		if err != nil {
			return nil, nil, err
		}
		if stored {
			return ring, provider, nil
		}
		// Another replica established the keyring first; serve with theirs.
		if entries, err = repository.ListKeyringEntries(ctx); err != nil {
			return nil, nil, err
		}
	}
	ring, err := keyring.Load(ctx, provider, entries)
	if err != nil {
		return nil, nil, fmt.Errorf("sealed — cannot unwrap the keyring: %w", err)
	}
	return ring, provider, nil
}

func initializeAudit(
	ctx context.Context,
	targets auditTargetLoader,
	logger *slog.Logger,
	hmacKeyVersion string,
	hmacKey []byte,
) (*audit.Broker, error) {
	configured, err := targets.ListAuditTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("load configured targets: %w", err)
	}
	if len(configured) != 0 && (hmacKeyVersion == "" || len(hmacKey) == 0) {
		return nil, errors.New("DFBG_AUDIT_HMAC_KEY and DFBG_AUDIT_HMAC_KEY_VERSION are required when audit is configured")
	}
	opened := make([]audit.Target, 0, len(configured))
	closeOpened := func() {
		for _, target := range opened {
			_ = target.Sink.Close(context.Background())
		}
	}
	for _, target := range configured {
		sink, err := audit.NewFileSink(target.Path, logger)
		if err != nil {
			closeOpened()
			return nil, fmt.Errorf("open configured target %q: %w", target.ID, err)
		}
		opened = append(opened, audit.Target{ID: target.ID, Sink: sink})
	}
	broker, err := audit.NewBroker(logger, opened...)
	if err != nil {
		closeOpened()
		return nil, err
	}
	return broker, nil
}

func configuredShutdownGracePeriod() (time.Duration, error) {
	configured := os.Getenv("DFBG_SHUTDOWN_GRACE_PERIOD")
	if configured == "" {
		return defaultShutdownGracePeriod, nil
	}
	period, err := time.ParseDuration(configured)
	if err != nil || period <= 0 {
		return 0, errors.New("DFBG_SHUTDOWN_GRACE_PERIOD must be a positive duration")
	}
	return period, nil
}

func shutdown(server, metricsServer *http.Server, broker *audit.Broker, deadline time.Time) error {
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	httpErr := server.Shutdown(ctx)
	var metricsErr error
	if metricsServer != nil {
		metricsErr = metricsServer.Shutdown(ctx)
	}
	auditErr := broker.Close(ctx)
	return errors.Join(httpErr, metricsErr, auditErr)
}

func auditHMACConfiguration() (string, []byte) {
	key := []byte(os.Getenv("DFBG_AUDIT_HMAC_KEY"))
	version := os.Getenv("DFBG_AUDIT_HMAC_KEY_VERSION")
	if len(key) == 0 || version == "" {
		return "", nil
	}
	return version, key
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:                         address,
		Handler:                      handler,
		DisableGeneralOptionsHandler: true,
		ReadHeaderTimeout:            5 * time.Second,
		ReadTimeout:                  30 * time.Second,
		WriteTimeout:                 60 * time.Second,
		IdleTimeout:                  120 * time.Second,
	}
}

const (
	rootRouteToken           = "root.token"
	rootRoutePacker          = "root.packer"
	rootRouteResourceManager = "root.resource_manager"
	rootRouteInit            = "root.init"
	rootRouteRecovery        = "root.recovery"
	rootRouteHealth          = "root.health"
	rootRouteSession         = "root.session"
	rootRoutePlatform        = "root.platform"
	rootRouteNotFound        = "root.not_found"
	rootRouteConsole         = "root.console"
	rootRouteRedirect        = "root.redirect"
	rootRouteUnhandled       = "root.unhandled"
)

var auditExemptRouteIDs = map[string]struct{}{
	rootRouteHealth: {},
	// The console handler serves only embedded static assets and its static SPA
	// entry point. Vault audits API requests rather than UI asset serving: these
	// requests carry no tenancy, state change, or credential, and every API
	// action taken from the console remains audited. Keeping the diagnostic UI
	// reachable during an audit outage is an operational property.
	rootRouteConsole: {},
}

type rootRoute struct {
	pattern    string
	apiVersion string
	descriptor audit.Descriptor
	handler    http.Handler
}

type describedHandler struct {
	descriptor audit.Descriptor
	next       http.Handler
}

func (h *describedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.next.ServeHTTP(w, r)
}

type rootRouter struct {
	mux *http.ServeMux
}

func (r *rootRouter) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.mux.ServeHTTP(w, request)
}

func (r *rootRouter) Resolve(request *http.Request) audit.Descriptor {
	// ServeMux handles RequestURI "*" before route matching. Naming that path
	// here keeps the resolver truthful to the handler that will actually run.
	if request.RequestURI == "*" {
		return audit.Descriptor{
			RouteID: rootRouteUnhandled, Operation: "request.reject", TargetType: "request",
		}
	}
	handler, pattern := r.mux.Handler(request)
	if described, ok := handler.(*describedHandler); ok {
		descriptor := described.descriptor
		if resolver, ok := described.next.(audit.Resolver); ok && !descriptor.Exempt {
			resolved := resolver.Resolve(request)
			resolved.RouteID = descriptor.RouteID
			resolved.Exempt = descriptor.Exempt
			if resolved.Operation != "" {
				return resolved
			}
		}
		return descriptor
	}
	if pattern != "" {
		return audit.Descriptor{
			RouteID: rootRouteRedirect, Operation: "request.redirect", TargetType: "request",
		}
	}
	return audit.Descriptor{
		RouteID: rootRouteUnhandled, Operation: "request.reject", TargetType: "request",
	}
}

type composedHandler struct {
	http.Handler
	resolver audit.Resolver
}

func (h *composedHandler) Resolve(request *http.Request) audit.Descriptor {
	return h.resolver.Resolve(request)
}

// composeHandler is the one production assembly point for admission, audit,
// root routing, and the four independently shaped HTTP planes.
type tokenPlane interface {
	http.Handler
	Admit(http.Handler, ...string) http.Handler
}

func composeHandler(
	broker *audit.Broker,
	hmacKey func() (string, []byte),
	token tokenPlane,
	packer, resourceManager, platformPlane http.Handler,
) *composedHandler {
	router := &rootRouter{mux: http.NewServeMux()}
	for _, route := range rootRoutes(token, packer, resourceManager, platformPlane) {
		router.mux.Handle(route.pattern, &describedHandler{
			descriptor: route.descriptor,
			next:       route.handler,
		})
	}
	// Recovery and cookie-session renewal share the token endpoint's per-caller
	// buckets: these anonymous minting surfaces must stay outside the audit seam
	// (ADR-0024; ADR-0020's amplification coupling).
	return &composedHandler{
		Handler: token.Admit(
			audit.NewHTTPHandler(broker, router, router, hmacKey),
			platform.RecoveryPath,
			http.MethodGet+" "+platform.SessionPath,
		),
		resolver: router,
	}
}

func rootRoutes(token, packer, resourceManager, platformPlane http.Handler) []rootRoute {
	return []rootRoute{
		newRootRoute(hcpauth.TokenPath, rootRouteToken, identity.AuditOperationTokenIssue, "access_token", "", token),
		newRootRoute(hcpauth.TokenPath+"/", rootRouteNotFound, "request.not_found", "request", "", http.NotFoundHandler()),
		newVersionedRootRoute("/packer/", "/packer/2023-01-01", rootRoutePacker, "packer.request", packer),
		// Resource-manager is how clients discover which tenants they may see.
		newVersionedRootRoute("/resource-manager/", "/resource-manager/2019-12-10", rootRouteResourceManager, "resource_manager.request", resourceManager),
		// Initialization authenticates itself by being permanently one-shot.
		newRootRoute("/sys/init", rootRouteInit, identity.AuditOperationInstanceInitialize, "instance", "singleton", platformPlane),
		// Recovery authenticates itself with the shares (ADR-0024).
		newRootRoute(platform.RecoveryPath, rootRouteRecovery, identity.AuditOperationInstanceRecover, "instance", "singleton", platformPlane),
		// A readiness probe has no credential and must survive audit degradation.
		newRootRoute(platform.StatusPath, rootRouteHealth, "health.read", "instance", "singleton", platformPlane),
		// Session routes carry the credential in a cookie or mint one into it.
		newRootRoute(platform.SessionPath, rootRouteSession, "session.request", "session", platform.SessionPath, platformPlane),
		newVersionedRootRoute("/api/v1/", "/api/v1", rootRoutePlatform, "platform.request", platformPlane),
		// Closed API subtrees must not become successful console HTML responses.
		newRootRoute("/sys/init/", rootRouteNotFound, "request.not_found", "request", "", http.NotFoundHandler()),
		newRootRoute(platform.RecoveryPath+"/", rootRouteNotFound, "request.not_found", "request", "", http.NotFoundHandler()),
		newRootRoute(platform.SessionPath+"/", rootRouteNotFound, "request.not_found", "request", "", http.NotFoundHandler()),
		newRootRoute("/api/v1", rootRouteNotFound, "request.not_found", "request", "", http.NotFoundHandler()),
		newRootRoute("/metrics", rootRouteNotFound, "request.not_found", "request", "", http.NotFoundHandler()),
		newRootRoute("/", rootRouteConsole, "console.serve", "console", "", web.NewHandler()),
	}
}

func newVersionedRootRoute(
	pattern, apiVersion, routeID string,
	operation identity.AuditOperation,
	handler http.Handler,
) rootRoute {
	route := newRootRoute(pattern, routeID, operation, "request", "", handler)
	route.apiVersion = apiVersion
	return route
}

func mountedAPIVersions(routes []rootRoute) []string {
	versions := make([]string, 0, len(routes))
	for _, route := range routes {
		if route.apiVersion != "" {
			versions = append(versions, route.apiVersion)
		}
	}
	return versions
}

func normalizedBuildValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func newRootRoute(
	pattern, routeID string, operation identity.AuditOperation,
	targetType, targetID string, handler http.Handler,
) rootRoute {
	_, exempt := auditExemptRouteIDs[routeID]
	descriptor := audit.Descriptor{
		RouteID: routeID, Exempt: exempt, Operation: operation,
		TargetType: targetType, TargetID: targetID,
	}
	if routeID == rootRouteNotFound {
		descriptor.HandlerlessReason = "not_found"
	}
	return rootRoute{
		pattern:    pattern,
		descriptor: descriptor,
		handler:    handler,
	}
}
