package cli

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/feiyu912/zenforge/configlayer"
	"github.com/feiyu912/zenforge/internal/dshapi"
	"github.com/feiyu912/zenforge/skill"
	skillfs "github.com/feiyu912/zenforge/skill/fs"
)

// buildSkillCatalog assembles the host's skill catalog from the two layers the
// command catalog reads: the workspace's own directory, which overrides the
// per-user one by name. A layer that does not exist is empty rather than an
// error -- most installations have no skills, and that is what a missing
// directory means. A layer that exists but cannot be scanned is an error, so a
// permission problem is reported instead of silently listing nothing.
func buildSkillCatalog(opts options) (skill.Catalog, error) {
	workspaceDir := strings.TrimSpace(opts.skillsDir)
	if workspaceDir == "" && strings.TrimSpace(opts.workspace) != "" {
		workspaceDir = filepath.Join(opts.workspace, skillfs.DefaultDir)
	}
	userDir := strings.TrimSpace(opts.userSkillsDir)
	if userDir == "" {
		configDir, err := configlayer.UserConfigDir()
		if err != nil {
			return nil, err
		}
		if configDir != "" {
			userDir = filepath.Join(configDir, "skills")
		}
	}
	layers := []struct {
		root   string
		source string
	}{
		{userDir, "user"},
		{workspaceDir, "project"},
	}
	catalogs := make([]skill.Catalog, 0, len(layers))
	for _, layer := range layers {
		catalog, err := openSkillLayer(layer.root, layer.source)
		if err != nil {
			return nil, err
		}
		if catalog != nil {
			catalogs = append(catalogs, catalog)
		}
	}
	// Merge sorts by name and lets the later layer win, so the workspace's skill
	// of a given name shadows the user's -- the same rule the command catalog
	// follows.
	return skill.Merge(catalogs...), nil
}

// openSkillLayer opens one layer, or reports nil when the directory is simply
// absent.
func openSkillLayer(root, source string) (skill.Catalog, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	catalog, err := skillfs.New(root, skillfs.Options{Source: source})
	if err != nil {
		return nil, err
	}
	return catalog, nil
}

// buildSkills freezes the catalog into the bundle a run advertises, or nil when
// the catalog is empty. A host with no skills must not advertise an empty
// catalog: the bundle's prompt would say "Available skills:" with nothing under
// it, and the run's fingerprint would claim a skill set that does not exist.
func buildSkills(ctx context.Context, opts options) (*skill.Bundle, error) {
	catalog, err := buildSkillCatalog(opts)
	if err != nil {
		return nil, err
	}
	items, err := catalog.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return skill.NewBundle(ctx, catalog, nil)
}

// consoleSkillCatalog adapts the catalog to the console's skills namespace. A
// catalog that cannot be scanned leaves the source nil, so the namespace answers
// with the dependency named rather than the panel reporting a failure it cannot
// explain -- the same rule the slash-command catalog follows.
func consoleSkillCatalog(opts options) dshapi.SkillSource {
	catalog, err := buildSkillCatalog(opts)
	if err != nil {
		slog.Warn("console skills are disabled: the skill catalog could not be read", "error", err)
		return nil
	}
	return consoleSkills{catalog: catalog}
}

// consoleSkills serves the console's skills panel from the host's catalog.
type consoleSkills struct {
	catalog skill.Catalog
}

// Skills lists the operator-facing rows: the skills a person may invoke, with the
// guidance the skill itself declares and whether the model may invoke it too. A
// skill hidden from the operator is left out -- upstream's own filter -- while a
// skill hidden only from the model stays listed, because that is exactly the row
// whose badge tells the operator the model will not pick it up on its own.
func (c consoleSkills) Skills(ctx context.Context) ([]dshapi.SkillInfo, error) {
	items, err := c.catalog.List(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]dshapi.SkillInfo, 0, len(items))
	for _, item := range items {
		if item.DisableUserInvocation {
			continue
		}
		rows = append(rows, dshapi.SkillInfo{
			Name:           item.Name,
			Description:    item.Description,
			WhenToUse:      item.WhenToUse,
			ModelInvocable: !item.DisableModelInvocation,
		})
	}
	return rows, nil
}
