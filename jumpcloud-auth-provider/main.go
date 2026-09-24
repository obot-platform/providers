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
	"strings"
	"time"

	oauth2proxy "github.com/oauth2-proxy/oauth2-proxy/v7"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/options"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/validation"
	"github.com/obot-platform/providers/auth-providers-common/pkg/env"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/obot-platform/providers/authcommon"
	"github.com/obot-platform/providers/jumpcloud-auth-provider/pkg/client"
	"github.com/obot-platform/providers/jumpcloud-auth-provider/pkg/profile"
)

type Options struct {
	ClientID                          string `env:"OBOT_JUMPCLOUD_AUTH_PROVIDER_CLIENT_ID"`
	ClientSecret                      string `env:"OBOT_JUMPCLOUD_AUTH_PROVIDER_CLIENT_SECRET"`
	IssuerURL                         string `env:"OBOT_JUMPCLOUD_AUTH_PROVIDER_ISSUER_URL" default:"https://oauth.id.jumpcloud.com/"`
	APIKey                            string `env:"OBOT_JUMPCLOUD_AUTH_PROVIDER_API_KEY" optional:"true"`
	APIBaseURL                        string `env:"OBOT_JUMPCLOUD_AUTH_PROVIDER_API_BASE_URL" optional:"true" default:"https://console.jumpcloud.com"`
	APIOrgID                          string `env:"OBOT_JUMPCLOUD_AUTH_PROVIDER_API_ORG_ID" optional:"true"`
	ServiceAccountClientID            string `env:"OBOT_JUMPCLOUD_AUTH_PROVIDER_SERVICE_ACCOUNT_CLIENT_ID" optional:"true"`
	ServiceAccountClientSecret        string `env:"OBOT_JUMPCLOUD_AUTH_PROVIDER_SERVICE_ACCOUNT_CLIENT_SECRET" optional:"true"`
	ServiceAccountTokenURL            string `env:"OBOT_JUMPCLOUD_AUTH_PROVIDER_SERVICE_ACCOUNT_TOKEN_URL" optional:"true" default:"https://admin-oauth.id.jumpcloud.com/oauth2/token"`
	ObotServerURL                     string `env:"OBOT_SERVER_PUBLIC_URL,OBOT_SERVER_URL"`
	PostgresConnectionDSN             string `env:"OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_DSN" optional:"true"`
	PostgresMaxConnections            int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_MAX_CONNECTIONS" optional:"true"`
	PostgresMaxIdleConnections        int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_MAX_IDLE_CONNECTIONS" optional:"true"`
	PostgresConnectionLifetimeSeconds int    `env:"OBOT_AUTH_PROVIDER_POSTGRES_CONNECTION_LIFETIME_SECONDS" optional:"true"`
	AuthCookieSecret                  string `usage:"Secret used to encrypt cookie" env:"OBOT_AUTH_PROVIDER_COOKIE_SECRET"`
	AuthEmailDomains                  string `usage:"Email domains allowed for authentication" default:"*" env:"OBOT_AUTH_PROVIDER_EMAIL_DOMAINS"`
	AuthTokenRefreshDuration          string `usage:"Duration to refresh auth token after" optional:"true" default:"1h" env:"OBOT_AUTH_PROVIDER_TOKEN_REFRESH_DURATION"`
	LoggingEnabled                    string `usage:"Enable oauth2-proxy logging" optional:"true" env:"OBOT_AUTH_PROVIDER_ENABLE_LOGGING"`
}

type server struct {
	apiClient *client.APIClient
	issuerURL string
}

func main() {
	var opts Options
	if err := env.LoadEnvForStruct(&opts); err != nil {
		fmt.Printf("ERROR: jumpcloud-auth-provider: failed to load options: %v\n", err)
		os.Exit(1)
	}

	opts.IssuerURL = strings.TrimSuffix(opts.IssuerURL, "/") + "/"
	opts.APIBaseURL = strings.TrimSuffix(opts.APIBaseURL, "/")
	opts.ServiceAccountTokenURL = strings.TrimSpace(opts.ServiceAccountTokenURL)

	refreshDuration, err := time.ParseDuration(opts.AuthTokenRefreshDuration)
	if err != nil {
		fmt.Printf("ERROR: jumpcloud-auth-provider: failed to parse token refresh duration: %v\n", err)
		os.Exit(1)
	}

	if refreshDuration < 0 {
		fmt.Printf("ERROR: jumpcloud-auth-provider: token refresh duration must be greater than 0\n")
		os.Exit(1)
	}

	cookieSecret, err := base64.StdEncoding.DecodeString(opts.AuthCookieSecret)
	if err != nil {
		fmt.Printf("ERROR: jumpcloud-auth-provider: failed to decode cookie secret: %v\n", err)
		os.Exit(1)
	}

	legacyOpts := options.NewLegacyOptions()
	legacyOpts.LegacyProvider.ProviderType = "oidc"
	legacyOpts.LegacyProvider.ProviderName = "oidc"
	legacyOpts.LegacyProvider.ClientID = opts.ClientID
	legacyOpts.LegacyProvider.ClientSecret = opts.ClientSecret
	legacyOpts.LegacyProvider.OIDCIssuerURL = opts.IssuerURL
	legacyOpts.LegacyProvider.Scope = "openid email profile offline_access"

	oauthProxyOpts, err := legacyOpts.ToOptions()
	if err != nil {
		fmt.Printf("ERROR: jumpcloud-auth-provider: failed to convert legacy options to new options: %v\n", err)
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
		oauthProxyOpts.Session.Postgres.TableNamePrefix = "jumpcloud_"
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
	loggingEnabled := strings.EqualFold(opts.LoggingEnabled, "true")
	oauthProxyOpts.Logging.RequestEnabled = loggingEnabled
	oauthProxyOpts.Logging.AuthEnabled = loggingEnabled
	oauthProxyOpts.Logging.StandardEnabled = loggingEnabled

	if err = validation.Validate(oauthProxyOpts); err != nil {
		fmt.Printf("ERROR: jumpcloud-auth-provider: failed to validate options: %v\n", err)
		os.Exit(1)
	}

	oauthProxy, err := oauth2proxy.NewOAuthProxy(oauthProxyOpts, oauth2proxy.NewValidator(oauthProxyOpts.EmailDomains, oauthProxyOpts.AuthenticatedEmailsFile))
	if err != nil {
		fmt.Printf("ERROR: jumpcloud-auth-provider: failed to create oauth2 proxy: %v\n", err)
		os.Exit(1)
	}

	hasAPIKey := strings.TrimSpace(opts.APIKey) != ""
	hasServiceAccount := strings.TrimSpace(opts.ServiceAccountClientID) != "" || strings.TrimSpace(opts.ServiceAccountClientSecret) != ""
	if !hasAPIKey && !hasServiceAccount {
		fmt.Printf("ERROR: jumpcloud-auth-provider: either OBOT_JUMPCLOUD_AUTH_PROVIDER_API_KEY or JumpCloud service-account credentials must be set\n")
		os.Exit(1)
	}
	if hasServiceAccount && (strings.TrimSpace(opts.ServiceAccountClientID) == "" || strings.TrimSpace(opts.ServiceAccountClientSecret) == "") {
		fmt.Printf("ERROR: jumpcloud-auth-provider: both OBOT_JUMPCLOUD_AUTH_PROVIDER_SERVICE_ACCOUNT_CLIENT_ID and OBOT_JUMPCLOUD_AUTH_PROVIDER_SERVICE_ACCOUNT_CLIENT_SECRET must be set together\n")
		os.Exit(1)
	}

	srv := &server{
		apiClient: client.New(opts.APIBaseURL, opts.APIKey, opts.APIOrgID, client.ServiceAccountOptions{
			ClientID:     opts.ServiceAccountClientID,
			ClientSecret: opts.ServiceAccountClientSecret,
			TokenURL:     opts.ServiceAccountTokenURL,
		}),
		issuerURL: opts.IssuerURL,
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "9999"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fmt.Appendf(nil, "http://127.0.0.1:%s", port))
	})
	mux.HandleFunc("/obot-get-state", state.ObotGetState(oauthProxy))
	mux.HandleFunc("/obot-get-user-info", srv.getUserInfo)
	mux.HandleFunc("/obot-list-auth-groups", authcommon.ListGroupsHandler("jumpcloud", srv.fetchGroupPage))
	mux.HandleFunc("/obot-get-auth-groups", authcommon.GetGroupsHandler("jumpcloud", srv.fetchGroupsByIDs))
	mux.HandleFunc("/obot-list-user-auth-groups", srv.listUserGroups)
	mux.HandleFunc("/", oauthProxy.ServeHTTP)

	listenHost := os.Getenv("OBOT_PROVIDER_LISTEN_HOST")
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}

	addr := listenHost + ":" + port
	if err := http.ListenAndServe(addr, mux); !errors.Is(err, http.ErrServerClosed) {
		fmt.Printf("ERROR: jumpcloud-auth-provider: failed to listen and serve: %v\n", err)
		os.Exit(1)
	}
}

// fetchGroupPage fetches one page of the JumpCloud organization's user groups. Paging, filtering
// and cursor handling all live in authcommon.ListGroupsHandler, which serves
// GET /obot-list-auth-groups.
func (s *server) fetchGroupPage(ctx context.Context, req authcommon.PageRequest) (authcommon.PageResult, error) {
	return profile.FetchGroupPage(ctx, s.apiClient, req)
}

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

	groups, err := profile.FetchUserGroupInfos(r.Context(), s.apiClient, userID)
	if err != nil {
		var unavailableErr *profile.UserUnavailableError
		if errors.As(err, &unavailableErr) {
			http.Error(w, unavailableErr.Error(), http.StatusForbidden)
			return
		}
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

func (s *server) getUserInfo(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		http.Error(w, "no authorization token provided", http.StatusUnauthorized)
		return
	}

	u, err := profile.GetUserInfo(r.Context(), token, s.issuerURL, s.apiClient)
	if err != nil {
		var unavailableErr *profile.UserUnavailableError
		if errors.As(err, &unavailableErr) {
			http.Error(w, unavailableErr.Error(), http.StatusForbidden)
			return
		}
		http.Error(w, fmt.Sprintf("failed to get user: %v", err), http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(w).Encode(u); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode user info: %v", err), http.StatusInternalServerError)
		return
	}
}

func (s *server) fetchGroupsByIDs(ctx context.Context, ids []string) (state.GroupInfoList, error) {
	return profile.FetchGroupsByIDs(ctx, s.apiClient, ids)
}
