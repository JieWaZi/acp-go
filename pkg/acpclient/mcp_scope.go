package acpclient

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"

	acp "github.com/JieWaZi/acp-go-sdk"
)

type mcpDirectory struct {
	// path identifies the canonical provider registry key.
	path string
	// info recognizes alternate spellings of the same directory.
	info os.FileInfo
	// hasRoutes distinguishes a session that can register directory-wide MCP servers.
	hasRoutes bool
	// routes identifies the exact native server descriptors without retaining secrets.
	routes [sha256.Size]byte
}

type directoryMCP struct {
	// directory retains the registration for as long as the native process can serve it.
	directory *mcpDirectory
	// sessionID is the only native session allowed to use the registered routes.
	sessionID acp.SessionId
}

func (c *Client) prepareMCPDirectory(request setupRequest) (*mcpDirectory, error) {
	if !c.opts.Protocol.DirectoryScopedMCP {
		return nil, nil
	}
	path, err := filepath.EvalSymlinks(request.cwd)
	if err != nil {
		return nil, invalid("ACP agent MCP namespace requires an existing working directory")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil, invalid("ACP agent MCP namespace requires an existing working directory")
	}
	scope := &mcpDirectory{path: path, info: info, hasRoutes: len(request.servers) > 0}
	if scope.hasRoutes {
		data, err := json.Marshal(request.servers)
		if err != nil {
			return nil, invalid("invalid ACP agent MCP descriptors")
		}
		scope.routes = sha256.Sum256(data)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.directoryMCP {
		if entry.directory.same(scope) {
			if entry.sessionID != request.id || !scope.hasRoutes || entry.directory.routes != scope.routes {
				return nil, invalid("ACP agent MCP namespace is already bound to a native session")
			}
			return scope, nil
		}
	}
	if scope.hasRoutes {
		if len(c.directoryMCP) >= maxSessions {
			return nil, invalid("ACP agent MCP namespace budget exhausted")
		}
		for _, existing := range c.sessions {
			if existing.id != request.id && scope.same(existing.mcpDirectory) {
				// Adding routes would also expose them to an already open session.
				return nil, invalid("ACP agent MCP namespace is shared with another native session")
			}
		}
	}
	return scope, nil
}

func (scope *mcpDirectory) same(other *mcpDirectory) bool {
	return other != nil && (scope.path == other.path || os.SameFile(scope.info, other.info))
}

// rememberMCPDirectory runs under session publication. Native close detaches a
// session but does not prove that the directory registration was removed.
func (c *Client) rememberMCPDirectory(s *session) {
	scope := s.mcpDirectory
	if scope == nil || !scope.hasRoutes {
		return
	}
	for _, entry := range c.directoryMCP {
		if entry.directory.same(scope) {
			return
		}
	}
	c.directoryMCP = append(c.directoryMCP, directoryMCP{directory: scope, sessionID: s.id})
}
