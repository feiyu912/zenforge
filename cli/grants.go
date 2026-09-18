package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/approval"
	approvalsqlite "github.com/feiyu912/zenforge/approval/sqlite"
)

// approvalGrantConfig opens the durable grant store the operator configured,
// and the namespace its grants belong to.
//
// Nothing is opened when no grants file is named: without one, a standing
// decision stays in the run that made it, which is where it has always lived.
// Naming a file is the whole opt-in.
func approvalGrantConfig(opts *options) (approval.GrantStore, approval.Namespace, error) {
	if opts == nil {
		return nil, approval.Namespace{}, nil
	}
	path := approvalGrantsPath(opts, "")
	if path == "" {
		return nil, approval.Namespace{}, nil
	}
	namespace, err := approvalNamespaceFor(opts, "", "")
	if err != nil {
		return nil, approval.Namespace{}, err
	}
	store, err := approvalsqlite.Open(context.Background(), path)
	if err != nil {
		return nil, approval.Namespace{}, fmt.Errorf("open approval grants %s: %w", path, err)
	}
	opts.addCloser("approval grants", store.Close)
	return store, namespace, nil
}

// approvalGrantsPath is the grants file a command should use: the explicit
// value when a flag supplied one, otherwise the configured file, otherwise
// nothing (and persistence stays off).
func approvalGrantsPath(opts *options, override string) string {
	if path := strings.TrimSpace(override); path != "" {
		return path
	}
	if opts == nil {
		return ""
	}
	return strings.TrimSpace(opts.approvalGrantsFile)
}

// approvalNamespaceFor resolves the namespace a grant belongs to: an explicit
// value first (a flag, then the configuration), and the operator's own
// identity last.
func approvalNamespaceFor(opts *options, tenant, subject string) (approval.Namespace, error) {
	if opts != nil {
		if strings.TrimSpace(tenant) == "" {
			tenant = opts.approvalTenant
		}
		if strings.TrimSpace(subject) == "" {
			subject = opts.approvalSubject
		}
	}
	tenant = strings.TrimSpace(tenant)
	subject = strings.TrimSpace(subject)
	if tenant == "" {
		tenant = "cli"
	}
	if subject == "" {
		// The file lives in the operator's own environment, so the default
		// subject is that user; the point of the namespace is that a grant
		// recorded for one identity is never replayed for another, and an
		// invented shared default would defeat it. If the user cannot be
		// determined, the operator has to say who this is.
		subject = strings.TrimSpace(os.Getenv("USER"))
		if subject == "" {
			subject = strings.TrimSpace(os.Getenv("USERNAME"))
		}
		if subject == "" {
			return approval.Namespace{}, fmt.Errorf(
				"approval.subject is required: this platform does not say who the grants file belongs to")
		}
	}
	namespace := approval.Namespace{Tenant: tenant, Subject: subject}
	if err := namespace.Validate(); err != nil {
		return approval.Namespace{}, err
	}
	return namespace, nil
}

// grantsCommand is the operator's view of the durable grant store: what
// standing approvals exist, and how to take one back.
//
// It reads the same file the agent writes, resolved the same way, so listing
// grants is not a second configuration to get wrong.
func grantsCommand(ctx context.Context, args []string, ioStreams IO) error {
	fs := flag.NewFlagSet("grants", flag.ContinueOnError)
	fs.SetOutput(ioStreams.Stderr)
	configPath := fs.String("config", "", "config file path")
	grantsFile := fs.String("grants-file", "", "approval grants database (default: approval.grantsFile)")
	tenant := fs.String("tenant", "", "grant namespace tenant")
	subject := fs.String("subject", "", "grant namespace subject")
	fingerprint := fs.String("fingerprint", "", "revoke a payload-pinned grant instead of the standing rule grant")
	all := fs.Bool("all", false, "revoke every live grant in the namespace")
	jsonOut := fs.Bool("json", false, "print JSON")
	// A CLI reads `grants revoke <rule> --config x` as naturally as
	// `grants --config x revoke <rule>`, so the flags are collected out of the
	// line before the flag set parses them: Go's flag package stops at the
	// first positional, and the verb is one.
	verb, flagArgs, positional := splitGrantsArgs(args)
	if err := fs.Parse(flagArgs); err != nil {
		return invalidUsage(err)
	}
	if verb == "" {
		return invalidUsage(errors.New("grants needs a subcommand: list|revoke"))
	}
	configArgs := []string{}
	if strings.TrimSpace(*configPath) != "" {
		configArgs = []string{"--config", *configPath}
	}
	opts, err := optionsFromArgs(configArgs)
	if err != nil {
		return err
	}
	path := approvalGrantsPath(&opts, *grantsFile)
	if path == "" {
		return invalidUsage(errors.New("no approval grants file configured: set approval.grantsFile or pass --grants-file"))
	}
	namespace, err := approvalNamespaceFor(&opts, *tenant, *subject)
	if err != nil {
		return invalidUsage(err)
	}
	store, err := approvalsqlite.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("open approval grants %s: %w", path, err)
	}
	defer func() { _ = store.Close() }()

	switch verb {
	case "list":
		if len(positional) != 0 {
			return invalidUsage(errors.New("grants list does not accept positional arguments"))
		}
		grants, err := store.List(ctx, namespace)
		if err != nil {
			return err
		}
		if *jsonOut {
			data, err := json.Marshal(grants)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(ioStreams.Stdout, string(data))
			return nil
		}
		if len(grants) == 0 {
			_, _ = fmt.Fprintf(ioStreams.Stdout, "no grants for %s/%s\n", namespace.Tenant, namespace.Subject)
			return nil
		}
		_, _ = fmt.Fprintln(ioStreams.Stdout, "RULE KEY\tSCOPE\tACTION\tGRANTED\tEXPIRES")
		for _, grant := range grants {
			expires := "never"
			if grant.ExpiresAt != nil {
				expires = grant.ExpiresAt.UTC().Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(ioStreams.Stdout, "%s\t%s\t%s\t%s\t%s\n",
				grantKeyLabel(grant),
				grant.EffectiveScope(),
				grant.Action,
				grant.GrantedAt.UTC().Format(time.RFC3339),
				expires,
			)
		}
		return nil
	case "revoke":
		if *all {
			if len(positional) != 0 {
				return invalidUsage(errors.New("grants revoke --all does not accept a rule key"))
			}
			return revokeAllGrants(ctx, store, namespace, ioStreams)
		}
		if len(positional) != 1 {
			return invalidUsage(errors.New("grants revoke needs exactly one rule key"))
		}
		ruleKey := strings.TrimSpace(positional[0])
		if err := store.Revoke(ctx, namespace, ruleKey, *fingerprint); err != nil {
			if errors.Is(err, approval.ErrGrantNotFound) {
				return fmt.Errorf("no %s grant for %q in %s/%s",
					grantScopeWord(*fingerprint), ruleKey, namespace.Tenant, namespace.Subject)
			}
			return err
		}
		_, _ = fmt.Fprintf(ioStreams.Stdout, "revoked %s %s\n", grantScopeWord(*fingerprint), ruleKey)
		return nil
	default:
		return invalidUsage(fmt.Errorf("unknown grants subcommand %q", verb))
	}
}

// grantsValueFlags are the grants flags that take a separate value, which is
// what splitting the line has to know to leave the value with its flag.
var grantsValueFlags = map[string]bool{
	"config": true, "grants-file": true, "tenant": true, "subject": true, "fingerprint": true,
}

// splitGrantsArgs separates the subcommand, the flags, and the positionals so
// they can appear in any order. The verb is the first token that is not a
// flag; everything else that is not a flag value is a positional.
func splitGrantsArgs(args []string) (verb string, flagArgs, positional []string) {
	for i := 0; i < len(args); i++ {
		token := args[i]
		if strings.HasPrefix(token, "-") {
			flagArgs = append(flagArgs, token)
			name := strings.TrimLeft(token, "-")
			if !strings.Contains(name, "=") && grantsValueFlags[name] && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		if verb == "" {
			verb = token
			continue
		}
		positional = append(positional, token)
	}
	return verb, flagArgs, positional
}

// grantKeyLabel shows the rule key, with the pinned fingerprint appended when
// there is one: a rule's standing grant and its payload-pinned entries share a
// rule key and are different entries.
func grantKeyLabel(grant approval.Grant) string {
	if strings.TrimSpace(grant.Fingerprint) == "" {
		return grant.RuleKey
	}
	return grant.RuleKey + " (" + grant.Fingerprint + ")"
}

func grantScopeWord(fingerprint string) string {
	if strings.TrimSpace(fingerprint) == "" {
		return "standing"
	}
	return "pinned"
}

func revokeAllGrants(ctx context.Context, store *approvalsqlite.Store, namespace approval.Namespace, ioStreams IO) error {
	grants, err := store.List(ctx, namespace)
	if err != nil {
		return err
	}
	if len(grants) == 0 {
		_, _ = fmt.Fprintf(ioStreams.Stdout, "no grants to revoke for %s/%s\n", namespace.Tenant, namespace.Subject)
		return nil
	}
	for _, grant := range grants {
		if err := store.Revoke(ctx, namespace, grant.RuleKey, grant.Fingerprint); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(ioStreams.Stdout, "revoked %d grant(s) for %s/%s\n", len(grants), namespace.Tenant, namespace.Subject)
	return nil
}
