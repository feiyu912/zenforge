package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

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
	path := strings.TrimSpace(opts.approvalGrantsFile)
	if path == "" {
		return nil, approval.Namespace{}, nil
	}
	subject := strings.TrimSpace(opts.approvalSubject)
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
			return nil, approval.Namespace{}, fmt.Errorf(
				"approval.subject is required: this platform does not say who the grants file belongs to")
		}
	}
	tenant := strings.TrimSpace(opts.approvalTenant)
	if tenant == "" {
		tenant = "cli"
	}
	namespace := approval.Namespace{Tenant: tenant, Subject: subject}
	if err := namespace.Validate(); err != nil {
		return nil, approval.Namespace{}, err
	}
	store, err := approvalsqlite.Open(context.Background(), path)
	if err != nil {
		return nil, approval.Namespace{}, fmt.Errorf("open approval grants %s: %w", path, err)
	}
	opts.addCloser("approval grants", store.Close)
	return store, namespace, nil
}
