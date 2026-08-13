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
	RegistryType         string             `json:"registryType"`
	RegistryBaseURL      string             `json:"registryBaseUrl,omitempty"`
	Identifier           string             `json:"identifier"`
	Version              string             `json:"version"`
	RuntimeHint          string             `json:"runtimeHint,omitempty"`
	Transport            registryTransport  `json:"transport"`
	RuntimeArguments     []registryArgument `json:"runtimeArguments,omitempty"`
	PackageArguments     []registryArgument `json:"packageArguments,omitempty"`
	EnvironmentVariables []registryEnvVar   `json:"environmentVariables,omitempty"`
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
	Official          registryMeta               `json:"io.modelcontextprotocol.registry/official"`
	PublisherProvided *registryPublisherProvided `json:"io.modelcontextprotocol.registry/publisher-provided,omitempty"`
}

type registryPublisherProvided struct {
	Categories []string `json:"categories,omitempty"`
	Keywords   []string `json:"keywords,omitempty"`
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
		hc:      NewHTTPClientWithTimeout(60 * time.Second),
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
	u.Path = "/v0.1/servers"
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
		return nil, fmt.Errorf("registry get: status %d (%s)", resp.StatusCode, readErrorSnippet(resp.Body))
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
// Each call fetches a single page from the Registry API using cursor-based pagination.
// When a query is provided, it's passed to the API for server-side filtering.
// The frontend is expected to pass back the NextPage cursor for subsequent pages.
//
// API 端点使用稳定的 /v0.1/servers (官方推荐);
// limit 上限 100,超过会被 API 拒绝(422),此处自动用 50 重试一次以增强健壮性。
func (f *RegistryFetcher) Search(ctx context.Context, opts SearchOptions) (*SearchResultServers, error) {
	normalizePaging(&opts)

	result, err := f.searchOnce(ctx, opts, opts.PageSize)
	if err != nil {
		// 422 通常是 limit 超过 API 上限,自动用更小的 limit 重试一次
		if isStatusErr(err, 422) && opts.PageSize > 50 {
			result, err = f.searchOnce(ctx, opts, 50)
		}
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// searchOnce 执行一次单页查询,limitBy 允许覆盖 opts.PageSize(用于 422 降级重试)
func (f *RegistryFetcher) searchOnce(ctx context.Context, opts SearchOptions, limitBy int) (*SearchResultServers, error) {
	if limitBy <= 0 {
		limitBy = opts.PageSize
	}
	u, err := url.Parse(f.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse registry base: %w", err)
	}
	u.Path = "/v0.1/servers"
	q := u.Query()
	q.Set("limit", fmt.Sprintf("%d", limitBy))
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if qs := strings.TrimSpace(opts.Query); qs != "" {
		q.Set("search", qs)
	}
	u.RawQuery = q.Encode()

	resp, err := f.hc.Get(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("registry search: %w", err)
	}

	if resp.StatusCode != 200 {
		snippet := readErrorSnippet(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("registry search: status %d (%s)", resp.StatusCode, snippet)
	}

	var listResp registryListResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 5*1024*1024)).Decode(&listResp); err != nil {
		drainBody(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("registry decode: %w", err)
	}
	resp.Body.Close()

	// Filter entries: when browsing (no query), only keep isLatest=true.
	// When searching, deduplicate by name but prefer isLatest=true version,
	// so the user sees the most recent data (tags, command, etc.) for each server.
	var items []MarketServer
	hasQuery := strings.TrimSpace(opts.Query) != ""
	bestEntries := make(map[string]registryServerEntry)
	var order []string
	for _, entry := range listResp.Servers {
		name := entry.Server.Name
		if name == "" {
			continue
		}
		if existing, ok := bestEntries[name]; !ok {
			order = append(order, name)
			bestEntries[name] = entry
		} else if entry.Meta.Official.IsLatest && !existing.Meta.Official.IsLatest {
			// Replace with the isLatest=true version
			bestEntries[name] = entry
		}
	}
	for _, name := range order {
		entry := bestEntries[name]
		// When browsing (no query), only include isLatest=true entries.
		if !hasQuery && !entry.Meta.Official.IsLatest {
			continue
		}
		items = append(items, *convertRegistryEntry(entry))
	}

	nextPage := ""
	hasMore := false
	if listResp.Metadata.NextCursor != "" {
		nextPage = listResp.Metadata.NextCursor
		hasMore = true
	}

	// 确保 items 非 nil,nil slice 会被 JSON 序列化为 null,导致前端 `...more.items` 崩溃
	if items == nil {
		items = []MarketServer{}
	}
	return &SearchResultServers{
		Items:    items,
		Total:    listResp.Metadata.Count,
		Page:     opts.Page,
		HasMore:  hasMore,
		NextPage: nextPage,
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
		Tags:        append(append(append([]string{}, s.Categories...), s.Tags...), entry.collectPublisherTags()...),
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

		// 将 registryType 作为标签添加（如 "npm"、"pypi"）
		if pkg.RegistryType != "" {
			ms.Tags = append(ms.Tags, pkg.RegistryType)
		}

		// Runtime arguments (e.g., -y for npx)
		var runtimeArgs []string
		for _, arg := range pkg.RuntimeArguments {
			if arg.Value != "" {
				runtimeArgs = append(runtimeArgs, arg.Value)
			}
		}
		// If no runtimeHint, derive command from registryType
		// Many servers omit runtimeHint but provide registryType + identifier
		if ms.Command == "" && pkg.RegistryType != "" {
			switch pkg.RegistryType {
			case "npm":
				ms.Command = "npx"
				if len(runtimeArgs) == 0 {
					runtimeArgs = []string{"-y"}
				}
			case "pypi":
				ms.Command = "uvx"
			case "oci":
				ms.Command = "docker"
				if len(runtimeArgs) == 0 {
					runtimeArgs = []string{"run"}
				}
			}
		}
		ms.Args = append(ms.Args, runtimeArgs...)
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
		case "streamable-http":
			ms.Transport = "streamable-http"
		case "sse":
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

// collectPublisherTags 从 publisher-provided 元数据中收集 categories 和 keywords 作为标签。
func (entry registryServerEntry) collectPublisherTags() []string {
	if entry.Meta.PublisherProvided == nil {
		return nil
	}
	var tags []string
	tags = append(tags, entry.Meta.PublisherProvided.Categories...)
	tags = append(tags, entry.Meta.PublisherProvided.Keywords...)
	return tags
}
