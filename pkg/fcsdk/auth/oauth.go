package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuthConfig configures an OAuthClient for the authorization-code flow.
type OAuthConfig struct {
	// IssuerURL is the FlowCatalyst OIDC server base URL.
	IssuerURL string
	// ClientID is the registered OAuth client id.
	ClientID string
	// ClientSecret is the OAuth client secret (omit for public clients).
	ClientSecret string
	// RedirectURI is the application's callback URL.
	RedirectURI string
	// Scopes requested. Defaults to ["openid", "profile", "email"].
	Scopes []string
	// HTTPClient overrides the default transport. Optional.
	HTTPClient *http.Client
}

// OAuthClient wraps the OAuth2 authorization-code flow + the OIDC
// session-end (RP-initiated logout) endpoint.
type OAuthClient struct {
	cfg  OAuthConfig
	http *http.Client
}

// NewOAuthClient builds an OAuthClient. Scopes default to openid/profile/email.
func NewOAuthClient(cfg OAuthConfig) *OAuthClient {
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email"}
	}
	http := cfg.HTTPClient
	if http == nil {
		http = defaultHTTPClient()
	}
	cfg.IssuerURL = strings.TrimRight(cfg.IssuerURL, "/")
	return &OAuthClient{cfg: cfg, http: http}
}

// AuthorizeParams is the session-stored side of an authorize-URL call.
type AuthorizeParams struct {
	PKCE  PkceChallenge
	State string
	Nonce string
}

// AuthorizeURL builds the URL to redirect users to for login, plus the
// session-stored verifier/state/nonce to validate the callback.
func (c *OAuthClient) AuthorizeURL() (string, AuthorizeParams) {
	pkce := NewPkceChallenge()
	state := randomURLSafe(32)
	nonce := randomURLSafe(32)

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", c.cfg.RedirectURI)
	q.Set("scope", strings.Join(c.cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", pkce.CodeChallenge)
	q.Set("code_challenge_method", pkce.CodeChallengeMethod)

	return c.cfg.IssuerURL + "/oauth/authorize?" + q.Encode(),
		AuthorizeParams{PKCE: pkce, State: state, Nonce: nonce}
}

// TokenResponse is the body of /oauth/token.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// ExchangeCode exchanges an authorization code for tokens. Call this
// from your callback handler after validating state and PKCE.
func (c *OAuthClient) ExchangeCode(ctx context.Context, code, codeVerifier string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.cfg.RedirectURI)
	form.Set("client_id", c.cfg.ClientID)
	form.Set("code_verifier", codeVerifier)
	if c.cfg.ClientSecret != "" {
		form.Set("client_secret", c.cfg.ClientSecret)
	}
	return c.postToken(ctx, form)
}

// RefreshToken exchanges a refresh token for a fresh access token.
func (c *OAuthClient) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", c.cfg.ClientID)
	if c.cfg.ClientSecret != "" {
		form.Set("client_secret", c.cfg.ClientSecret)
	}
	return c.postToken(ctx, form)
}

// RevokeToken revokes an access or refresh token per RFC 7009. A 200
// response is required; anything else returns an Error.
func (c *OAuthClient) RevokeToken(ctx context.Context, token string) error {
	form := url.Values{}
	form.Set("token", token)
	form.Set("client_id", c.cfg.ClientID)
	if c.cfg.ClientSecret != "" {
		form.Set("client_secret", c.cfg.ClientSecret)
	}
	resp, err := c.postForm(ctx, c.cfg.IssuerURL+"/oauth/revoke", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return newErr(KindTokenExchange, fmt.Sprintf("revoke failed (%d): %s", resp.StatusCode, body))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// IntrospectionResponse is the body of /oauth/introspect (RFC 7662).
type IntrospectionResponse struct {
	Active    bool   `json:"active"`
	Scope     string `json:"scope,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	Username  string `json:"username,omitempty"`
	TokenType string `json:"token_type,omitempty"`
	Exp       int64  `json:"exp,omitempty"`
	Iat       int64  `json:"iat,omitempty"`
	Sub       string `json:"sub,omitempty"`
}

// IntrospectToken posts to /oauth/introspect and returns the response.
func (c *OAuthClient) IntrospectToken(ctx context.Context, token string) (*IntrospectionResponse, error) {
	form := url.Values{}
	form.Set("token", token)
	form.Set("client_id", c.cfg.ClientID)
	if c.cfg.ClientSecret != "" {
		form.Set("client_secret", c.cfg.ClientSecret)
	}
	resp, err := c.postForm(ctx, c.cfg.IssuerURL+"/oauth/introspect", form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, newErr(KindTokenExchange, fmt.Sprintf("introspect failed (%d): %s", resp.StatusCode, body))
	}
	var out IntrospectionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, newErr(KindTokenExchange, "parse introspect response: "+err.Error())
	}
	return &out, nil
}

// UserInfoResponse is the body of /oauth/userinfo. Extra carries any
// custom claims the platform adds.
type UserInfoResponse struct {
	Sub           string         `json:"sub"`
	Name          string         `json:"name,omitempty"`
	Email         string         `json:"email,omitempty"`
	EmailVerified *bool          `json:"email_verified,omitempty"`
	Extra         map[string]any `json:"-"`
}

// UnmarshalJSON keeps standard fields typed and bucketing extras.
func (u *UserInfoResponse) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	get := func(k string, dst any) error {
		v, ok := raw[k]
		if !ok {
			return nil
		}
		delete(raw, k)
		return json.Unmarshal(v, dst)
	}
	if err := get("sub", &u.Sub); err != nil {
		return err
	}
	if err := get("name", &u.Name); err != nil {
		return err
	}
	if err := get("email", &u.Email); err != nil {
		return err
	}
	if err := get("email_verified", &u.EmailVerified); err != nil {
		return err
	}
	if len(raw) > 0 {
		u.Extra = make(map[string]any, len(raw))
		for k, v := range raw {
			var any any
			if err := json.Unmarshal(v, &any); err != nil {
				return err
			}
			u.Extra[k] = any
		}
	}
	return nil
}

// UserInfo fetches /oauth/userinfo using the access token.
func (c *OAuthClient) UserInfo(ctx context.Context, accessToken string) (*UserInfoResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.IssuerURL+"/oauth/userinfo", nil)
	if err != nil {
		return nil, newErr(KindTokenExchange, err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, newErr(KindTokenExchange, err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, newErr(KindTokenExchange, fmt.Sprintf("userinfo failed (%d): %s", resp.StatusCode, body))
	}
	var out UserInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, newErr(KindTokenExchange, "parse userinfo response: "+err.Error())
	}
	return &out, nil
}

// LogoutURL builds the RP-Initiated Logout URL.
//
// When postLogoutRedirectURI is non-empty, idTokenHint must also be set
// — FlowCatalyst uses the hint's aud claim to verify the redirect URI
// against the client's registered postLogoutRedirectUris (OIDC RP-
// Initiated Logout 1.0 §2). Omitting the hint causes the OP to refuse
// the redirect.
func (c *OAuthClient) LogoutURL(postLogoutRedirectURI, idTokenHint, state string) string {
	base := c.cfg.IssuerURL + "/auth/oidc/session/end"
	q := url.Values{}
	if postLogoutRedirectURI != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirectURI)
	}
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}
	if state != "" {
		q.Set("state", state)
	}
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}

// postToken posts a form to /oauth/token and decodes the TokenResponse.
func (c *OAuthClient) postToken(ctx context.Context, form url.Values) (*TokenResponse, error) {
	resp, err := c.postForm(ctx, c.cfg.IssuerURL+"/oauth/token", form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, newErr(KindTokenExchange, fmt.Sprintf("token exchange failed (%d): %s", resp.StatusCode, body))
	}
	var out TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, newErr(KindTokenExchange, "parse token response: "+err.Error())
	}
	return &out, nil
}

func (c *OAuthClient) postForm(ctx context.Context, fullURL string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, newErr(KindTokenExchange, err.Error())
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, newErr(KindTokenExchange, err.Error())
	}
	return resp, nil
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}
