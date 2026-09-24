package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/configlayer"
	"github.com/feiyu912/zenforge/server/auth"
	"github.com/feiyu912/zenforge/server/harnesshttp"
)

const (
	// authTokensFileName is where the tokens this host accepts live when the
	// operator did not name a file: the host configuration directory, beside the
	// console's settings document, so an operator has one place to know about.
	authTokensFileName = "tokens.json"
	// authTokenReloadInterval is how stale the in-memory token set may be. A
	// running host has to see a mint or a revoke an operator made in another
	// process -- `zenforge token revoke` is useless if the host keeps honouring
	// the token until it is restarted -- and a file that holds a handful of rows
	// is cheap enough to re-read on a short interval rather than on every request.
	authTokenReloadInterval = 2 * time.Second
	// authAuditFileName is the default audit trail, used whenever authentication
	// is required and the operator did not name another path.
	authAuditFileName = "audit.jsonl"
)

// serveAuthOptions is what the serve flags decided about caller identity. It is a
// struct rather than five parameters so the resolution below reads as one
// decision: who this host will serve.
type serveAuthOptions struct {
	// requireAuth is --require-auth, the operator's explicit decision.
	requireAuth bool
	// allowRemote is --allow-remote, which now also requires tokens unless the
	// operator says otherwise: binding a non-loopback address used to expose every
	// route, including the settings API and the whole console, to anyone who could
	// reach the port.
	allowRemote bool
	// allowAnonymousRemote is the explicit escape hatch for a network the operator
	// already trusts (a private bridge, a VPN), so the old behaviour stays
	// reachable without being the default.
	allowAnonymousRemote bool
	// tokenFile is --auth-token-file; empty means the host configuration
	// directory.
	tokenFile string
	// auditLog is --audit-log; empty means the host configuration directory when
	// authentication is required, and no trail otherwise.
	auditLog string
	// workspace is the directory this host serves, used to refuse a secret-bearing
	// file the console could read back to a browser.
	workspace string
}

// serveAuth is the assembled caller-identity decision a served host runs with.
// It is always non-nil in production, even when this host requires nothing: a
// token that was offered is still verified, attributed and audited, and the audit
// trail is what makes a deployment reviewable.
type serveAuth struct {
	required bool
	tokens   *auth.TokenStore
	audit    *auth.AuditLog
	// authn is the one authenticator every route shares, so the cookie the
	// sign-in form sets is the very credential the console's RPC carries.
	authn  *auth.Authenticator
	policy auth.Policy
}

// authenticator is the shared authenticator. A nil host gets an empty one rather
// than a nil dereference: an unconfigured test that only wants the routing table
// must not have to build a token store to ask for it.
func (a *serveAuth) authenticator() *auth.Authenticator {
	if a == nil || a.authn == nil {
		return &auth.Authenticator{}
	}
	return a.authn
}

// resolveServeAuth turns the flags into the decision. It fails closed: a host
// that is asked to require tokens and holds none refuses to start, because it
// could only refuse every caller, and a host that requires authentication and
// cannot keep a trail refuses too, because an unaccountable deployment is exactly
// what the requirement was for.
func resolveServeAuth(options serveAuthOptions) (*serveAuth, error) {
	required := options.requireAuth || (options.allowRemote && !options.allowAnonymousRemote)

	tokenPath, err := authFilePath(options.tokenFile, authTokensFileName)
	if err != nil {
		return nil, err
	}
	if tokenPath != "" {
		if err := refuseAuthFileInWorkspace("--auth-token-file", tokenPath, options.workspace); err != nil {
			return nil, invalidUsage(err)
		}
	}
	var tokens *auth.TokenStore
	if tokenPath != "" {
		tokens, err = auth.OpenTokenStore(tokenPath)
		if err != nil {
			return nil, err
		}
	}
	if required && (tokens == nil || tokens.Len() == 0) {
		if tokenPath == "" {
			return nil, invalidUsage(errors.New(
				"authentication is required but this host has no place to keep tokens: pass --auth-token-file <path>"))
		}
		return nil, invalidUsage(fmt.Errorf(
			"authentication is required but %s holds no token: mint one with `zenforge token create --tenant <tenant> --subject <subject> --token-file %s`, or pass --allow-anonymous-remote if this network is already trusted",
			tokenPath, tokenPath))
	}

	var auditLog *auth.AuditLog
	auditPath := strings.TrimSpace(options.auditLog)
	if auditPath == "" && required {
		auditPath, err = authFilePath("", authAuditFileName)
		if err != nil {
			return nil, err
		}
		if auditPath == "" {
			return nil, invalidUsage(errors.New(
				"authentication is required but this host has no place to keep an audit trail: pass --audit-log <path>"))
		}
	}
	if auditPath != "" {
		if err := refuseAuthFileInWorkspace("--audit-log", auditPath, options.workspace); err != nil {
			return nil, invalidUsage(err)
		}
		auditLog, err = auth.OpenAuditLog(auditPath)
		if err != nil {
			return nil, err
		}
	}

	authenticator := &auth.Authenticator{Store: tokens, TTL: auth.DefaultSessionTTL}
	return &serveAuth{
		required: required,
		tokens:   tokens,
		audit:    auditLog,
		authn:    authenticator,
		policy: auth.Policy{
			Require: required,
			Auth:    authenticator,
			Audit:   auditLog,
		},
	}, nil
}

// watchTokens re-reads the token file on an interval until the context ends, so a
// token minted or revoked in another process takes effect within
// authTokenReloadInterval instead of at the next restart. The request path reads
// the in-memory set; this is what keeps that set from being stale.
func (a *serveAuth) watchTokens(ctx context.Context, interval time.Duration) {
	if a == nil || a.tokens == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.tokens.Reload(); err != nil {
				// A reload failure keeps the loaded set, so the host keeps serving
				// the tokens it last read; saying so is what keeps that from being
				// silent.
				slog.Default().Error("auth: reloading the token file failed",
					"path", a.tokens.Path(), "error", err)
			}
		}
	}
}

// wrap is the only layer that sees every route this host serves, which is why it
// is the one that decides whether a request is served at all.
func (a *serveAuth) wrap(next http.Handler) http.Handler {
	if a == nil {
		return next
	}
	return a.policy.Middleware(next)
}

// accessController carries the identity the boundary resolved into the runs this
// host starts through server/harnesshttp's routes, so a /runs/start caller's
// approval grants are recorded under that caller's namespace. It never refuses:
// the boundary already did, and a second opinion here would be a second policy to
// keep in step.
func (a *serveAuth) accessController() harnesshttp.AccessController {
	if a == nil {
		return nil
	}
	return harnesshttp.AccessFunc(func(ctx context.Context, _ *http.Request, _ harnesshttp.Operation) (harnesshttp.AccessDecision, error) {
		namespace, ok := approval.NamespaceFrom(ctx)
		if !ok {
			return harnesshttp.AccessDecision{}, nil
		}
		return harnesshttp.AccessDecision{ApprovalNamespace: namespace}, nil
	})
}

// close releases the audit trail this host opened.
func (a *serveAuth) close() error {
	if a == nil || a.audit == nil {
		return nil
	}
	return a.audit.Close()
}

// authFilePath is where a secret-bearing file lives: the explicit path is the
// operator's decision and is taken verbatim (made absolute, so a host that
// changes directory cannot write it somewhere else), and otherwise the file goes
// into the host configuration directory -- the same one the CLI's user config
// layer and the console's settings document already use. An empty path with a nil
// error means this host has no durable home and the caller has to decide what to
// do about that.
func authFilePath(explicit, name string) (string, error) {
	if path := strings.TrimSpace(explicit); path != "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		return absolute, nil
	}
	configDir, err := configlayer.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the host configuration directory: %w", err)
	}
	if strings.TrimSpace(configDir) == "" {
		return "", nil
	}
	return filepath.Join(configDir, name), nil
}

// refuseAuthFileInWorkspace keeps a token file or an audit trail out of the
// workspace this host serves. The console reads workspace files back to the
// browser (ADR 0089), so a credentials file inside one is a browsable document --
// the same reason the settings document is refused there.
func refuseAuthFileInWorkspace(flagName, path, workspace string) error {
	if !pathIsInsideWorkspace(path, workspace) {
		return nil
	}
	return fmt.Errorf(
		"%s %s is inside the workspace this host serves (%s), and the console reads workspace files back to the browser: keep credentials outside the workspace, or point the host at another directory with --workspace",
		flagName, filepath.Clean(path), filepath.Clean(workspace))
}

// tokenCommand is the operator's own view of the tokens this host accepts. It is
// the only way a token is minted: the plaintext exists in this command's output
// and nowhere else, which is why nothing else in this program can create one.
func tokenCommand(ctx context.Context, args []string, ioStreams IO) error {
	if len(args) == 0 {
		return invalidUsage(errors.New("token needs a subcommand: create, list, or revoke"))
	}
	switch strings.TrimSpace(args[0]) {
	case "create":
		return tokenCreate(args[1:], ioStreams)
	case "list":
		return tokenList(args[1:], ioStreams)
	case "revoke":
		return tokenRevoke(args[1:], ioStreams)
	default:
		return invalidUsage(fmt.Errorf("unknown token subcommand %q: use create, list, or revoke", args[0]))
	}
}

// tokenCreate mints one token and prints it once. The store keeps only its hash,
// so a lost token is replaced rather than recovered.
func tokenCreate(args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	tokenFile := fs.String("token-file", "", "file the tokens are kept in; defaults to tokens.json in the host configuration directory")
	tenant := fs.String("tenant", "", "tenant this token authenticates as (required)")
	subject := fs.String("subject", "", "subject within the tenant, such as a person or a service (required)")
	note := fs.String("note", "", "free-form note kept beside the token")
	jsonOut := fs.Bool("json", false, "print the minted token as one JSON object")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	store, err := openTokenStore(*tokenFile)
	if err != nil {
		return err
	}
	secret, token, err := store.Create(*tenant, *subject, *note)
	if err != nil {
		return err
	}
	if *jsonOut {
		body, err := json.Marshal(map[string]any{
			"id":        token.ID,
			"tenant":    token.Tenant,
			"subject":   token.Subject,
			"token":     secret,
			"createdAt": token.CreatedAt.UTC().Format(time.RFC3339),
			"note":      token.Note,
		})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(ioStreams.Stdout, string(body))
		return nil
	}
	_, _ = fmt.Fprintf(ioStreams.Stdout, "id:      %s\n", token.ID)
	_, _ = fmt.Fprintf(ioStreams.Stdout, "tenant:  %s\n", token.Tenant)
	_, _ = fmt.Fprintf(ioStreams.Stdout, "subject: %s\n", token.Subject)
	_, _ = fmt.Fprintf(ioStreams.Stdout, "token:   %s\n", secret)
	_, _ = fmt.Fprintf(ioStreams.Stdout, "\n%s keeps only the hash of this token, so this is the only time it is shown.\n", store.Path())
	_, _ = fmt.Fprintln(ioStreams.Stdout, "Present it as `Authorization: Bearer <token>`, or paste it into a served console's /auth page.")
	return nil
}

// tokenList reports the tokens this host accepts. A hash is not printed: an
// operator needs to know which credential exists, not to read it.
func tokenList(args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("token list", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	tokenFile := fs.String("token-file", "", "file the tokens are kept in; defaults to tokens.json in the host configuration directory")
	jsonOut := fs.Bool("json", false, "print the tokens as one JSON array")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	store, err := openTokenStore(*tokenFile)
	if err != nil {
		return err
	}
	tokens := store.List()
	if *jsonOut {
		type row struct {
			ID        string `json:"id"`
			Tenant    string `json:"tenant"`
			Subject   string `json:"subject"`
			CreatedAt string `json:"createdAt"`
			Note      string `json:"note,omitempty"`
		}
		rows := make([]row, 0, len(tokens))
		for _, token := range tokens {
			rows = append(rows, row{
				ID:        token.ID,
				Tenant:    token.Tenant,
				Subject:   token.Subject,
				CreatedAt: token.CreatedAt.UTC().Format(time.RFC3339),
				Note:      token.Note,
			})
		}
		body, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(ioStreams.Stdout, string(body))
		return nil
	}
	if len(tokens) == 0 {
		_, _ = fmt.Fprintf(ioStreams.Stdout, "no tokens in %s\n", store.Path())
		return nil
	}
	_, _ = fmt.Fprintln(ioStreams.Stdout, "ID\tTENANT\tSUBJECT\tCREATED\tNOTE")
	for _, token := range tokens {
		_, _ = fmt.Fprintf(ioStreams.Stdout, "%s\t%s\t%s\t%s\t%s\n",
			token.ID,
			token.Tenant,
			token.Subject,
			token.CreatedAt.UTC().Format(time.RFC3339),
			token.Note,
		)
	}
	_, _ = fmt.Fprintf(ioStreams.Stdout, "\n%d token(s) in %s\n", len(tokens), store.Path())
	return nil
}

// tokenRevoke invalidates one token by id. Every session that signed in with it
// ends with it, because the browser session is the token.
func tokenRevoke(args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	tokenFile := fs.String("token-file", "", "file the tokens are kept in; defaults to tokens.json in the host configuration directory")
	id := fs.String("id", "", "id of the token to revoke (required)")
	if err := fs.Parse(args); err != nil {
		return invalidUsage(err)
	}
	tokenID := strings.TrimSpace(*id)
	if tokenID == "" {
		return invalidUsage(errors.New("token revoke needs --id; `zenforge token list` prints them"))
	}
	store, err := openTokenStore(*tokenFile)
	if err != nil {
		return err
	}
	if err := store.Revoke(tokenID); err != nil {
		if errors.Is(err, auth.ErrTokenNotFound) {
			return fmt.Errorf("no token %s in %s", tokenID, store.Path())
		}
		return err
	}
	_, _ = fmt.Fprintf(ioStreams.Stdout, "revoked %s\n", tokenID)
	return nil
}

// openTokenStore opens the token file a command was pointed at. A file that does
// not exist yet is created empty by the store: an operator minting the first
// token should not have to create it by hand.
func openTokenStore(explicit string) (*auth.TokenStore, error) {
	path, err := authFilePath(explicit, authTokensFileName)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("this host has no configuration directory to keep tokens in: pass --token-file <path>")
	}
	return auth.OpenTokenStore(path)
}
