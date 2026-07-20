package ansibleproject

import (
	"io/fs"
	"os"
	"path"
	"strings"
)

// Config locates the Ansible content root this Syncer reads. The root is a Git
// checkout / mounted directory — read-only (§1.2, §2.5): the plugin holds NO write
// credential to it, never clones, never writes; it only parses and projects.
type Config struct {
	// Root is the filesystem path to the Ansible content root (the SoR).
	Root string
	// ProjectID qualifies every projected identity so two content roots' identically
	// named files never collide in one estate (mirrors AWX's controller qualifier).
	// "" ⇒ the base name of Root.
	ProjectID string
	// FS overrides the filesystem for tests; nil ⇒ os.DirFS(Root). Keeps the parser
	// exercisable against an in-memory tree with no real disk.
	FS fs.FS
}

// Client is a read-only reader of an Ansible content root. It is the plugin's own
// SoR integration (module isolation, ADR-0046) — it imports nothing from core/.
type Client struct {
	fsys      fs.FS
	projectID string
}

// New builds a read client rooted at cfg.Root (or cfg.FS for tests).
func New(cfg Config) *Client {
	fsys := cfg.FS
	if fsys == nil {
		fsys = os.DirFS(cfg.Root)
	}
	pid := cfg.ProjectID
	if pid == "" {
		pid = path.Base(strings.TrimRight(cfg.Root, "/"))
		if pid == "" || pid == "." || pid == "/" {
			pid = "project"
		}
	}
	return &Client{fsys: fsys, projectID: pid}
}

// ProjectID is the qualifier prefixed onto every projected identity.
func (c *Client) ProjectID() string { return c.projectID }

// qualify project-namespaces a relative content path: "<projectID>/<rel>".
func (c *Client) qualify(rel string) string { return c.projectID + "/" + rel }
