package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

// smitheryBaseURL 是 Smithery Registry API 的基地址（包级变量便于测试覆盖）
var smitheryBaseURL = "https://registry.smithery.ai"

// SmitheryFetcher 调用 Smithery Registry API（匿名访问）
type SmitheryFetcher struct {
	hc *HTTPClient
}

func NewSmitheryFetcher() *SmitheryFetcher {
	return &SmitheryFetcher{hc: NewHTTPClient()}
}

func (f *SmitheryFetcher) Source() Source { return SourceSmithery }

// smitherySearchResponse 是 Smithery Registry 搜索响应
type smitherySearchResponse struct {
	Servers    []smitheryServer   `json:"servers"`
	Pagination smitheryPagination `json:"pagination"`
}

type smitheryPagination struct {
	CurrentPage int `json:"currentPage"`
	PageSize    int `json:"pageSize"`
	TotalPages  int `json:"totalPages"`
	TotalCount  int `json:"totalCount"`
}

// smitheryServer 是 Smithery 搜索结果中的单个服务器
type smitheryServer struct {
	ID            string `json:"id"`
	QualifiedName string `json:"qualifiedName"`
	Namespace     string `json:"namespace"`
	DisplayName   string `json:"displayName"`
	Description   string `json:"description"`
	Homepage      string `json:"homepage"`
	IconURL       string `json:"iconUrl"`
	// UseCount 在 API 中为 number 类型，用 json.Number 兼容 string/number 两种形式
	UseCount   json.Number `json:"useCount"`
	IsDeployed bool        `json:"isDeployed"`
	Remote     bool        `json:"remote"`
	Verified   bool        `json:"verified"`
	BySmithery bool        `json:"bySmithery"`
	Owner      string      `json:"owner"`
	CreatedAt  string      `json:"createdAt"`
}

// smitheryDetailResponse 是 Smithery 单服务器详情响应
type smitheryDetailResponse struct {
	QualifiedName string               `json:"qualifiedName"`
	DisplayName   string               `json:"displayName"`
	Description   string               `json:"description"`
	Homepage      string               `json:"homepage"`
	DeploymentURL string               `json:"deploymentUrl"`
	Remote        bool                 `json:"remote"`
	Connections   []smitheryConnection `json:"connections"`
}

type smitheryConnection struct {
	Type          string          `json:"type"` // "http" | "stdio"
	DeploymentURL string          `json:"deploymentUrl"`
	ConfigSchema  json.RawMessage `json:"configSchema"`
}

func (f *SmitheryFetcher) Search(ctx context.Context, opts SearchOptions) (*SearchResultServers, error) {
	normalizePaging(&opts)

	q := url.Values{}
	if opts.Query != "" {
		q.Set("q", opts.Query)
	}
	q.Set("page", strconv.Itoa(opts.Page))
	q.Set("pageSize", strconv.Itoa(opts.PageSize))

	endpoint := fmt.Sprintf("%s/servers?%s", smitheryBaseURL, q.Encode())
	resp, err := f.hc.Get(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("smithery fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		return nil, fmt.Errorf("smithery fetch: status %d", resp.StatusCode)
	}

	var body smitherySearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&body); err != nil {
		return nil, fmt.Errorf("smithery decode: %w", err)
	}

	result := &SearchResultServers{
		Items: make([]MarketServer, 0, len(body.Servers)),
		Page:  body.Pagination.CurrentPage,
	}
	for _, item := range body.Servers {
		srv := convertSmithery(item)
		srv.Source = SourceSmithery
		srv.SourceID = item.QualifiedName
		result.Items = append(result.Items, srv)
	}
	result.Total = body.Pagination.TotalCount
	result.HasMore = body.Pagination.CurrentPage < body.Pagination.TotalPages
	if result.HasMore {
		result.NextPage = strconv.Itoa(body.Pagination.CurrentPage + 1)
	}
	return result, nil
}

// Get 拉取单个服务器详情，按连接类型构造 stdio/http 模板
func (f *SmitheryFetcher) Get(ctx context.Context, qualifiedName string) (*MarketServer, error) {
	endpoint := fmt.Sprintf("%s/servers/%s", smitheryBaseURL, url.PathEscape(qualifiedName))
	resp, err := f.hc.Get(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("smithery get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		drainBody(resp.Body)
		if resp.StatusCode == 404 {
			return nil, fmt.Errorf("smithery server %s not found", qualifiedName)
		}
		return nil, fmt.Errorf("smithery get: status %d", resp.StatusCode)
	}

	var detail smitheryDetailResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&detail); err != nil {
		return nil, fmt.Errorf("smithery decode: %w", err)
	}

	srv := MarketServer{
		Name:        detail.QualifiedName,
		Title:       detail.DisplayName,
		Description: detail.Description,
		Homepage:    detail.Homepage,
		Source:      SourceSmithery,
		SourceID:    detail.QualifiedName,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	// 优先选择第一个可用连接构造模板
	if len(detail.Connections) > 0 {
		conn := detail.Connections[0]
		switch conn.Type {
		case "http":
			srv.Transport = "http"
			srv.URL = conn.DeploymentURL
			if srv.URL == "" {
				srv.URL = detail.DeploymentURL
			}
		case "stdio":
			srv.Transport = "stdio"
			srv.Command = "npx"
			srv.Args = []string{"-y", "smithery@latest", "run", detail.QualifiedName}
			// 从 configSchema 提取环境变量 key（值为空，前端让用户填）
			srv.Env = extractSmitheryEnvKeys(conn.ConfigSchema)
		}
	}

	// 默认 transport
	if srv.Transport == "" {
		srv.Transport = "stdio"
	}

	return &srv, nil
}

// convertSmithery 将 Smithery 搜索结果项转换为统一 MarketServer
func convertSmithery(s smitheryServer) MarketServer {
	return MarketServer{
		Name:        s.QualifiedName,
		Title:       s.DisplayName,
		Description: s.Description,
		Homepage:    s.Homepage,
		Installs:    parseSmitheryUseCount(s.UseCount),
		UpdatedAt:   s.CreatedAt,
		// Smithery 特有字段（用于前端筛选）
		BySmithery: s.BySmithery,
		IsDeployed: s.IsDeployed,
		IsVerified: s.Verified,
		IsRemote:   s.Remote,
	}
}

// parseSmitheryUseCount 将 json.Number 转为 int，失败或为负返回 0
func parseSmitheryUseCount(n json.Number) int {
	if n == "" {
		return 0
	}
	i, err := n.Int64()
	if err != nil || i < 0 {
		return 0
	}
	return int(i)
}

// extractSmitheryEnvKeys 从 configSchema 的 properties 中提取环境变量名（值留空）
func extractSmitheryEnvKeys(schema json.RawMessage) map[string]string {
	if len(schema) == 0 {
		return nil
	}
	var parsed struct {
		Properties map[string]struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		return nil
	}
	if len(parsed.Properties) == 0 {
		return nil
	}
	env := make(map[string]string, len(parsed.Properties))
	for key := range parsed.Properties {
		env[key] = ""
	}
	return env
}
