package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// Registry API base URL (package-level var for test override)
var registryAPIBase = "https://registry.modelcontextprotocol.io"

// --- API Response Types ---

type registryListResponse struct {
	Servers  []registryServerEntry `json:"servers"`
	Metadata registryMetadata      `json:"metadata"`
}

type registryMetadata struct {
	NextCursor string `json:"nextCursor,omitempty"`
	Count      int    `json:"count"`
}

type registryServerEntry struct {
	Server registryServer      `json:"server"`
	Meta   registryMetaWrapper `json:"_meta"`
}

type registryServer struct {
	Name        string            `json:"name"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	WebsiteURL  string            `json:"websiteUrl,omitempty"`
	Icon        *registryIcon     `json:"icon,omitempty"`
	Repository  *registryRepo     `json:"repository,omitempty"`
	Packages    []registryPackage `json:"packages,omitempty"`
	Remotes     []registryRemote  `json:"remotes,omitempty"`
	Categories  []string          `json:"categories,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
}

type registryIcon struct {
	Src      string `json:"src"`
	MimeType string `json:"mimeType,omitempty"`
}

type registryRepo struct {
	URL       string `json:"url"`
	Source    string `json:"source"`
	Subfolder string `json:"subfolder,omitempty"`
}

type registryPackage struct {
	RegistryType         string               `json:"registryType"`
	RegistryBaseURL      string               `json:"registryBaseUrl,omitempty"`
	Identifier           string               `json:"identifier"`
	Version              string               `json:"version"`
	RuntimeHint          string               `json:"runtimeHint,omitempty"`
	Transport            registryTransport    `json:"transport"`
	RuntimeArguments     []registryArgument   `json:"runtimeArguments,omitempty"`
	PackageArguments     []registryArgument   `json:"packageArguments,omitempty"`
	EnvironmentVariables []registryEnvVar     `json:"environmentVariables,omitempty"`
}

type registryTransport struct {
	Type string `json:"type"`
}

type registryArgument struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Value       string `json:"value,omitempty"`
	IsRepeated  bool   `json:"isRepeated,omitempty"`
	IsRequired  bool   `json:"isRequired,omitempty"`
	IsSecret    bool   `json:"isSecret,omitempty"`
	Description string `json:"description,omitempty"`
}

type registryEnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsRequired  bool   `json:"isRequired,omitempty"`
	IsSecret    bool   `json:"isSecret,omitempty"`
	Default     string `json:"default,omitempty"`
}

type registryRemote struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type registryMetaWrapper struct {
	Official registryMeta `json:"io.modelcontextprotocol.registry/official"`
}

type registryMeta struct {
	Status    string `json:"status"`
	IsLatest  bool   `json:"isLatest"`
	UpdatedAt string `json:"updatedAt"`
}

// --- Fetcher ---

type RegistryFetcher struct {
	hc      *HTTPClient
	baseURL string
}

func NewRegistryFetcher() *RegistryFetcher {
	return &RegistryFetcher{
		hc:      NewHTTPClient(),
		baseURL: registryAPIBase,
	}
}

func (f *RegistryFetcher) Source() Source { return SourceOfficial }

// Get retrieves a single server from the registry by its exact name.
func (f *RegistryFetcher) Get(ctx context.Context, sourceID string) (*MarketServer, error) {
	u, err := url.Parse(f.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse registry base: %w", err)
	}
	u.Path = "/v0/servers"
	q := u.Query()
	q.Set("search", sourceID)
	q.Set("limit", "10")
	u.RawQuery = q.Encode()
	resp, err := f.hc.Get(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("registry get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return nil, fmt.Errorf("registry get: status %d", resp.StatusCode)
	}

	var listResp registryListResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1*1024*1024)).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("registry decode: %w", err)
	}

	// Find exact match by server name with isLatest=true
	for _, entry := range listResp.Servers {
		if entry.Server.Name == sourceID && entry.Meta.Official.IsLatest {
			return convertRegistryEntry(entry), nil
		}
	}

	return nil, fmt.Errorf("server %q not found in registry", sourceID)
}

// Search queries the registry for servers matching the given options.
func (f *RegistryFetcher) Search(ctx context.Context, opts SearchOptions) (*SearchResultServers, error) {
	var allItems []MarketServer
	seen := make(map[string]bool)
	cursor := ""

	for {
		u, err := url.Parse(f.baseURL)
		if err != nil {
			return nil, fmt.Errorf("parse registry base: %w", err)
		}
		u.Path = "/v0/servers"
		query := u.Query()
		query.Set("limit", "100")
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		if qs := strings.TrimSpace(opts.Query); qs != "" {
			query.Set("search", qs)
		}
		u.RawQuery = query.Encode()

		resp, err := f.hc.Get(ctx, u.String())
		if err != nil {
			return nil, fmt.Errorf("registry search: %w", err)
		}

		if resp.StatusCode != 200 {
			drainBody(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("registry search: status %d", resp.StatusCode)
		}

		var listResp registryListResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 5*1024*1024)).Decode(&listResp); err != nil {
			drainBody(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("registry decode: %w", err)
		}
		resp.Body.Close()

		// Filter to only include entries where IsLatest is true, dedup by name
		for _, entry := range listResp.Servers {
			if !entry.Meta.Official.IsLatest {
				continue
			}
			if entry.Server.Name == "" || seen[entry.Server.Name] {
				continue
			}
			seen[entry.Server.Name] = true
			allItems = append(allItems, *convertRegistryEntry(entry))
		}

		// Check for more pages
		if listResp.Metadata.NextCursor == "" {
			break
		}
		cursor = listResp.Metadata.NextCursor
	}

	return &SearchResultServers{
		Items: allItems,
		Total: len(allItems),
		Page:  1,
	}, nil
}

// convertRegistryEntry converts a registry API server entry to a MarketServer.
func convertRegistryEntry(entry registryServerEntry) *MarketServer {
	s := entry.Server
	ms := &MarketServer{
		ID:          "registry:" + s.Name,
		SourceID:    s.Name,
		Description: s.Description,
		Homepage:    s.WebsiteURL,
		Tags:        append(append([]string{}, s.Categories...), s.Tags...),
		UpdatedAt:   entry.Meta.Official.UpdatedAt,
		Source:      SourceOfficial,
	}

	// Repository URL as docs
	if s.Repository != nil && s.Repository.URL != "" {
		ms.Docs = s.Repository.URL
	}

	// Use Title if available, otherwise last segment of the name
	if s.Title != "" {
		ms.Name = s.Title
		ms.Title = s.Title
	} else {
		parts := strings.Split(s.Name, "/")
		ms.Name = parts[len(parts)-1]
	}

	// Extract transport and command from packages
	if len(s.Packages) > 0 {
		pkg := s.Packages[0]
		ms.Command = pkg.RuntimeHint
		ms.Transport = pkg.Transport.Type

		// Runtime arguments (e.g., -y for npx)
		for _, arg := range pkg.RuntimeArguments {
			if arg.Value != "" {
				ms.Args = append(ms.Args, arg.Value)
			}
		}
		// Package identifier (e.g., package-name)
		if pkg.Identifier != "" {
			ms.Args = append(ms.Args, pkg.Identifier)
		}
		// Package arguments (e.g., --port 8080)
		for _, arg := range pkg.PackageArguments {
			if arg.Name != "" && arg.Value != "" {
				ms.Args = append(ms.Args, arg.Name, arg.Value)
			} else if arg.Value != "" {
				ms.Args = append(ms.Args, arg.Value)
			}
		}

		// Environment variables
		if len(pkg.EnvironmentVariables) > 0 {
			ms.Env = make(map[string]string, len(pkg.EnvironmentVariables))
			for _, ev := range pkg.EnvironmentVariables {
				ms.Env[ev.Name] = ev.Default
			}
		}
	}

	// If no packages, check remotes for URL-based transport
	if len(s.Packages) == 0 && len(s.Remotes) > 0 {
		ms.URL = s.Remotes[0].URL
		switch s.Remotes[0].Type {
		case "streamable-http", "sse":
			ms.Transport = "sse"
		default:
			ms.Transport = s.Remotes[0].Type
		}
	}

	// Parse updatedAt
	if entry.Meta.Official.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339, entry.Meta.Official.UpdatedAt); err == nil {
			ms.UpdatedAt = t.Format(time.RFC3339)
		}
	}

	return ms
}