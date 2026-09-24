package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	oauth2proxy "github.com/oauth2-proxy/oauth2-proxy/v7"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/options"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/validation"
	"github.com/obot-platform/providers/auth-providers-common/pkg/env"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/obot-platform/providers/auth0-auth-provider/pkg/client"
	"github.com/obot-platform/providers/auth0-auth-provider/pkg/profile"
	"github.com/obot-platform/providers/authcommon"
)

type Options struct {
	ClientID                          string `env:"OBOT_AUTH0_AUTH_PROVIDER_CLIENT_ID"`
	ClientSecret                      string `env:"OBOT_AUTH0_AUTH_PROVIDER_CLIENT_SECRET"`
	Domain                            string `env:"OBOT_AUTH0_AUTH_PROVIDER_DOMAIN"`
	ObotServerURL                     string `env:"OBOT_SERVER_PUBLIC_URL,OBOT_SERVER_URL"`
	PostgresConnectionDSN             string `env:"OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_DSN" optional:"true"`
	PostgresMaxConnections            int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_MAX_CONNECTIONS" optional:"true"`
	PostgresMaxIdleConnections        int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_MAX_IDLE_CONNECTIONS" optional:"true"`
	PostgresConnectionLifetimeSeconds int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_LIFETIME_SECONDS" optional:"true"`
	AuthCookieSecret                  string `usage:"Secret used to encrypt cookie" env:"OBOT_AUTH_PROVIDER_COOKIE_SECRET"`
	AuthEmailDomains                  string `usage:"Email domains allowed for authentication" default:"*" env:"OBOT_AUTH_PROVIDER_EMAIL_DOMAINS"`
	AuthTokenRefreshDuration          string `usage:"Duration to refresh auth token after" optional:"true" default:"1h" env:"OBOT_AUTH_PROVIDER_TOKEN_REFRESH_DURATION"`

	// InsecureOIDCAllowUnverifiedEmail allows logins where the IdP does not assert email_verified=true.
	// This weakens account/email-domain validation. Only enable if your Auth0 tenant requires it.
	InsecureOIDCAllowUnverifiedEmail string `env:"OBOT_AUTH0_AUTH_PROVIDER_INSECURE_ALLOW_UNVERIFIED_EMAIL" default:"false" optional:"true"`

	// Management API credentials for listing roles and user memberships.
	// These are marked optional to make migration easier, but are enforced as required by the Obot API.
	MgmtClientID     string `env:"OBOT_AUTH0_AUTH_PROVIDER_MGMT_CLIENT_ID" optional:"true"`
	MgmtClientSecret string `env:"OBOT_AUTH0_AUTH_PROVIDER_MGMT_CLIENT_SECRET" optional:"true"`
}

type server struct {
	mgmtClient *client.ManagementClient
	domain     string
}

func main() {
	var opts Options
	if err := env.LoadEnvForStruct(&opts); err != nil {
		fmt.Printf("ERROR: auth0-auth-provider: failed to load options: %v\n", err)
		os.Exit(1)
	}

	opts.Domain = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(opts.Domain, "https://"), "http://"), "/")

	refreshDuration, err := time.ParseDuration(opts.AuthTokenRefreshDuration)
	if err != nil {
		fmt.Printf("ERROR: auth0-auth-provider: failed to parse token refresh duration: %v\n", err)
		os.Exit(1)
	}

	if refreshDuration < 0 {
		fmt.Printf("ERROR: auth0-auth-provider: token refresh duration must be greater than 0\n")
		os.Exit(1)
	}

	cookieSecret, err := base64.StdEncoding.DecodeString(opts.AuthCookieSecret)
	if err != nil {
		fmt.Printf("ERROR: auth0-auth-provider: failed to decode cookie secret: %v\n", err)
		os.Exit(1)
	}

	legacyOpts := options.NewLegacyOptions()
	legacyOpts.LegacyProvider.ProviderType = "oidc"
	legacyOpts.LegacyProvider.ProviderName = "oidc"
	legacyOpts.LegacyProvider.ClientID = opts.ClientID
	legacyOpts.LegacyProvider.ClientSecret = opts.ClientSecret
	legacyOpts.LegacyProvider.OIDCIssuerURL = fmt.Sprintf("https://%s/", opts.Domain)
	legacyOpts.LegacyProvider.Scope = "openid email profile offline_access"
	insecureAllowUnverified, _ := strconv.ParseBool(opts.InsecureOIDCAllowUnverifiedEmail)
	legacyOpts.LegacyProvider.InsecureOIDCAllowUnverifiedEmail = insecureAllowUnverified

	oauthProxyOpts, err := legacyOpts.ToOptions()
	if err != nil {
		fmt.Printf("ERROR: auth0-auth-provider: failed to convert legacy options to new options: %v\n", err)
		os.Exit(1)
	}

	oauthProxyOpts.Server.BindAddress = ""
	oauthProxyOpts.MetricsServer.BindAddress = ""
	if opts.PostgresConnectionDSN != "" {
		oauthProxyOpts.Session.Type = options.PostgresSessionStoreType
		oauthProxyOpts.Session.Postgres.ConnectionDSN = opts.PostgresConnectionDSN
		oauthProxyOpts.Session.Postgres.MaxOpenConns = opts.PostgresMaxConnections
		oauthProxyOpts.Session.Postgres.MaxIdleConns = opts.PostgresMaxIdleConnections
		oauthProxyOpts.Session.Postgres.ConnMaxLifetime = opts.PostgresConnectionLifetimeSeconds
		oauthProxyOpts.Session.Postgres.TableNamePrefix = "auth0_"
	}
	oauthProxyOpts.Cookie.Refresh = refreshDuration
	oauthProxyOpts.Cookie.Name = "obot_access_token"
	oauthProxyOpts.Cookie.Secret = string(cookieSecret)
	oauthProxyOpts.Cookie.Secure = strings.HasPrefix(opts.ObotServerURL, "https://")
	oauthProxyOpts.RawRedirectURL = opts.ObotServerURL + "/"
	if opts.AuthEmailDomains != "" {
		emailDomains := strings.Split(opts.AuthEmailDomains, ",")
		for i := range emailDomains {
			emailDomains[i] = strings.TrimSpace(emailDomains[i])
		}
		oauthProxyOpts.EmailDomains = emailDomains
	}
	oauthProxyOpts.Logging.RequestEnabled = false
	oauthProxyOpts.Logging.AuthEnabled = false
	oauthProxyOpts.Logging.StandardEnabled = false

	if err = validation.Validate(oauthProxyOpts); err != nil {
		fmt.Printf("ERROR: auth0-auth-provider: failed to validate options: %v\n", err)
		os.Exit(1)
	}

	oauthProxy, err := oauth2proxy.NewOAuthProxy(oauthProxyOpts, oauth2proxy.NewValidator(oauthProxyOpts.EmailDomains, oauthProxyOpts.AuthenticatedEmailsFile))
	if err != nil {
		fmt.Printf("ERROR: auth0-auth-provider: failed to create oauth2 proxy: %v\n", err)
		os.Exit(1)
	}

	// Initialize Management API client for role and user lookups
	if opts.MgmtClientID == "" || opts.MgmtClientSecret == "" {
		fmt.Printf("ERROR: auth0-auth-provider: management API credentials OBOT_AUTH0_AUTH_PROVIDER_MGMT_CLIENT_ID and OBOT_AUTH0_AUTH_PROVIDER_MGMT_CLIENT_SECRET must be set\n")
		os.Exit(1)
	}
	mgmtClient := client.NewManagementClient(opts.Domain, opts.MgmtClientID, opts.MgmtClientSecret)

	srv := &server{
		mgmtClient: mgmtClient,
		domain:     opts.Domain,
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "9999"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprintf("http://127.0.0.1:%s", port)))
	})
	mux.HandleFunc("/obot-get-state", state.ObotGetState(oauthProxy))
	mux.HandleFunc("/obot-get-user-info", srv.getUserInfo)
	mux.HandleFunc("/obot-list-auth-groups", authcommon.ListGroupsHandler("auth0", srv.fetchGroupPage))
	mux.HandleFunc("/obot-get-auth-groups", authcommon.GetGroupsHandler("auth0", srv.fetchGroupsByIDs))
	mux.HandleFunc("/obot-list-user-auth-groups", srv.listUserGroups)
	mux.HandleFunc("/", oauthProxy.ServeHTTP)

	listenHost := os.Getenv("OBOT_PROVIDER_LISTEN_HOST")
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}

	addr := listenHost + ":" + port
	fmt.Printf("listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); !errors.Is(err, http.ErrServerClosed) {
		fmt.Printf("ERROR: auth0-auth-provider: failed to listen and serve: %v\n", err)
		os.Exit(1)
	}
}

// fetchGroupPage fetches one page of the Auth0 tenant's roles. Paging, filtering and cursor
// handling all live in authcommon.ListGroupsHandler, which serves GET /obot-list-auth-groups.
func (s *server) fetchGroupPage(ctx context.Context, req authcommon.PageRequest) (authcommon.PageResult, error) {
	return profile.FetchGroupPage(ctx, s.mgmtClient, req)
}

// listUserGroups returns all roles assigned to the specified user.
// Accepts a plain text body containing the user ID.
func (s *server) listUserGroups(w http.ResponseWriter, r *http.Request) {
	userIDBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	userID := strings.TrimSpace(string(userIDBytes))
	if userID == "" {
		http.Error(w, "user ID is required in request body", http.StatusBadRequest)
		return
	}

	groups, err := profile.FetchUserGroupInfos(r.Context(), s.mgmtClient, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to fetch user groups: %v", err), http.StatusInternalServerError)
		return
	}

	if groups == nil {
		groups = state.GroupInfoList{}
	}

	if err := json.NewEncoder(w).Encode(groups); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode groups: %v", err), http.StatusInternalServerError)
		return
	}
}

// getUserInfo returns user information including ID, name, and profile picture.
func (s *server) getUserInfo(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		http.Error(w, "no authorization token provided", http.StatusUnauthorized)
		return
	}

	u, err := profile.GetUserInfo(r.Context(), token, s.domain)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get user: %v", err), http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(w).Encode(u); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode user info: %v", err), http.StatusInternalServerError)
		return
	}
}

func (s *server) fetchGroupsByIDs(ctx context.Context, ids []string) (state.GroupInfoList, error) {
	return profile.FetchGroupsByIDs(ctx, s.mgmtClient, ids)
}
