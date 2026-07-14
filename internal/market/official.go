package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
)

var officialBaseURL = "https://registry.modelcontextprotocol.io"

type OfficialFetcher struct {
	hc *HTTPClient
}

func NewOfficialFetcher() *OfficialFetcher {
	return &OfficialFetcher{hc: NewHTTPClient()}
}

func (f *OfficialFetcher) Source() Source { return SourceOfficial }

type officialListResponse struct {
	Servers  []officialListItem `json:"servers"`
	Metadata *struct {
		Count      int    `json:"count"`
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

type officialListItem struct {
	Server officialServerDetail `json:"server"`
	Meta   map[string]any       `json:"_meta"`
}

type officialServerDetail struct {
	Schema      string            `json:"$schema"`
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Website     string            `json:"websiteUrl"`
	Repo        *officialRepo     `json:"repository"`
	Packages    []officialPackage `json:"packages"`
	Remotes     []officialRemote  `json:"remotes"`
}

type officialRepo struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

type officialPackage struct {
	RegistryName         string             `json:"registryName"`
	Identifier           string             `json:"identifier"`
	Version              string             `json:"version"`
	RuntimeHint          string             `json:"runtimeHint"`
	Transport            *officialTransport `json:"transport"`
	EnvironmentVariables []map[string]any   `json:"environmentVariables"`
}

type officialTransport struct {
	Type    string   `json:"type"`
	URL     string   `json:"url,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type officialRemote struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func (f *OfficialFetcher) Search(ctx context.Context, opts SearchOptions) (*SearchResultServers, error) {
	normalizePaging(&opts)

	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", opts.PageSize))
	q.Set("version", "latest")
	if opts.Query != "" {
		q.Set("search", opts.Query)
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}

	endpoint := fmt.Sprintf("%s/v0.1/servers?%s", officialBaseURL, q.Encode())
	resp, err := f.hc.Get(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("official fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return nil, fmt.Errorf("official fetch: status %d", resp.StatusCode)
	}

	var body officialListResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&body); err != nil {
		return nil, fmt.Errorf("official decode: %w", err)
	}

	result := &SearchResultServers{
		Items: make([]MarketServer, 0, len(body.Servers)),
		Page:  opts.Page,
	}
	for _, item := range body.Servers {
		srv := convertOfficial(item.Server)
		srv.Source = SourceOfficial
		srv.SourceID = item.Server.Name
		result.Items = append(result.Items, srv)
	}
	if body.Metadata != nil {
		result.Total = body.Metadata.Count
		result.HasMore = body.Metadata.NextCursor != ""
		result.NextPage = body.Metadata.NextCursor
	}
	return result, nil
}

func (f *OfficialFetcher) Get(ctx context.Context, sourceID string) (*MarketServer, error) {
	u := fmt.Sprintf("%s/v0.1/servers/%s/versions/latest", officialBaseURL, url.PathEscape(sourceID))
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	resp, err := f.hc.Get(ctx, parsed.String())
	if err != nil {
		return nil, fmt.Errorf("official get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		if resp.StatusCode == 404 {
			return nil, fmt.Errorf("server %s not found", sourceID)
		}
		return nil, fmt.Errorf("official get: status %d", resp.StatusCode)
	}

	var wrap struct {
		Server officialServerDetail `json:"server"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&wrap); err != nil {
		return nil, fmt.Errorf("official decode: %w", err)
	}
	srv := convertOfficial(wrap.Server)
	srv.Source = SourceOfficial
	srv.SourceID = wrap.Server.Name
	return &srv, nil
}

func convertOfficial(d officialServerDetail) MarketServer {
	srv := MarketServer{
		Name:        d.Name,
		Title:       d.Title,
		Description: d.Description,
		Homepage:    d.Website,
	}
	if d.Repo != nil {
		srv.Docs = d.Repo.URL
	}

	if len(d.Remotes) > 0 {
		srv.Transport = normalizeOfficialTransport(d.Remotes[0].Type)
		srv.URL = d.Remotes[0].URL
	}

	if len(d.Packages) > 0 {
		pkg := d.Packages[0]
		srv.Registry = pkg.RegistryName
		if pkg.Transport != nil {
			srv.Transport = normalizeOfficialTransport(pkg.Transport.Type)
			srv.Command = pkg.Transport.Command
			srv.Args = pkg.Transport.Args
		}
		if srv.Command == "" {
			switch pkg.RegistryName {
			case "npm":
				srv.Command = pkg.RuntimeHint
				if srv.Command == "" {
					srv.Command = "npx"
				}
				srv.Args = append([]string{"-y", pkg.Identifier}, srv.Args...)
			case "pypi":
				srv.Command = pkg.RuntimeHint
				if srv.Command == "" {
					srv.Command = "uvx"
				}
				srv.Args = append([]string{pkg.Identifier}, srv.Args...)
			case "oci", "docker":
				srv.Command = "docker"
				srv.Args = []string{"run", "-i", "--rm", pkg.Identifier}
			}
		}
		for _, ev := range pkg.EnvironmentVariables {
			if name, ok := ev["name"].(string); ok {
				if srv.Env == nil {
					srv.Env = make(map[string]string)
				}
				srv.Env[name] = ""
			}
		}
	}

	if srv.Transport == "" {
		srv.Transport = "stdio"
	}
	return srv
}

// normalizeOfficialTransport 归一化 transport 类型：
// - streamable-http / http-streamable → http
// - sse → sse
// - stdio → stdio
// 用于前端筛选下拉选项的一致性
func normalizeOfficialTransport(t string) string {
	switch t {
	case "streamable-http", "http-streamable":
		return "http"
	default:
		return t
	}
}
