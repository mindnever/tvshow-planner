// Package jellyfin is a small read-only client for the Jellyfin HTTP API.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrBadCredentials is returned when Jellyfin rejects the login or token.
var ErrBadCredentials = errors.New("jellyfin rejected the credentials")

type Client struct {
	BaseURL  string
	Token    string
	UserID   string
	DeviceID string
	HTTP     *http.Client
}

// NormalizeURL validates a user-supplied server address.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", errors.New("server address must look like https://jellyfin.example.com")
	}
	return raw, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	auth := fmt.Sprintf(`MediaBrowser Client="tvshow-planner", Device="server", DeviceId=%q, Version="1.0"`, c.DeviceID)
	if c.Token != "" {
		auth += fmt.Sprintf(`, Token=%q`, c.Token)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, ErrBadCredentials
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, fmt.Errorf("jellyfin: %s %s: %s", method, path, resp.Status)
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

// ErrQuickConnectUnavailable is returned when the server has Quick Connect
// turned off.
var ErrQuickConnectUnavailable = errors.New("quick connect is not enabled on the Jellyfin server")

// authResult reads the response of either login endpoint into c.
func (c *Client) authResult(resp *http.Response) (username string, err error) {
	defer resp.Body.Close()
	var out struct {
		AccessToken string
		User        struct{ Id, Name string }
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("jellyfin: unexpected login response: %w", err)
	}
	if out.AccessToken == "" || out.User.Id == "" {
		return "", errors.New("jellyfin: login response had no token (is this a Jellyfin server?)")
	}
	c.Token, c.UserID = out.AccessToken, out.User.Id
	return out.User.Name, nil
}

// Authenticate logs in with a username and password. The password is not
// retained; the returned client carries the access token Jellyfin issued.
func Authenticate(ctx context.Context, hc *http.Client, baseURL, username, password, deviceID string) (*Client, string, error) {
	c := &Client{BaseURL: baseURL, DeviceID: deviceID, HTTP: hc}
	resp, err := c.do(ctx, http.MethodPost, "/Users/AuthenticateByName", nil,
		map[string]string{"Username": username, "Pw": password})
	if err != nil {
		return nil, "", err
	}
	name, err := c.authResult(resp)
	return c, name, err
}

// QuickConnect is a pending Quick Connect login. The user approves it by
// entering Code in a Jellyfin client where they are already signed in.
type QuickConnect struct {
	Secret string
	Code   string
}

// StartQuickConnect begins a Quick Connect login for the given device.
func StartQuickConnect(ctx context.Context, hc *http.Client, baseURL, deviceID string) (QuickConnect, error) {
	c := &Client{BaseURL: baseURL, DeviceID: deviceID, HTTP: hc}
	// Jellyfin 10.9+ wants POST; older servers only accept GET.
	resp, err := c.do(ctx, http.MethodPost, "/QuickConnect/Initiate", nil, nil)
	if err != nil && !errors.Is(err, ErrBadCredentials) {
		resp, err = c.do(ctx, http.MethodGet, "/QuickConnect/Initiate", nil, nil)
	}
	if errors.Is(err, ErrBadCredentials) {
		return QuickConnect{}, ErrQuickConnectUnavailable
	}
	if err != nil {
		return QuickConnect{}, err
	}
	defer resp.Body.Close()
	var qc QuickConnect
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&qc); err != nil || qc.Secret == "" || qc.Code == "" {
		return QuickConnect{}, errors.New("jellyfin: unexpected Quick Connect response")
	}
	return qc, nil
}

// QuickConnectApproved reports whether the user has approved the request.
func QuickConnectApproved(ctx context.Context, hc *http.Client, baseURL, deviceID, secret string) (bool, error) {
	c := &Client{BaseURL: baseURL, DeviceID: deviceID, HTTP: hc}
	var out struct{ Authenticated bool }
	err := c.getJSON(ctx, "/QuickConnect/Connect", url.Values{"secret": {secret}}, &out)
	return out.Authenticated, err
}

// AuthenticateQuickConnect exchanges an approved Quick Connect request for
// an access token.
func AuthenticateQuickConnect(ctx context.Context, hc *http.Client, baseURL, deviceID, secret string) (*Client, string, error) {
	c := &Client{BaseURL: baseURL, DeviceID: deviceID, HTTP: hc}
	resp, err := c.do(ctx, http.MethodPost, "/Users/AuthenticateWithQuickConnect", nil, map[string]string{"Secret": secret})
	if err != nil {
		return nil, "", err
	}
	name, err := c.authResult(resp)
	return c, name, err
}

// ServerName returns the name the Jellyfin server was given by its admin.
// It needs no login.
func ServerName(ctx context.Context, hc *http.Client, baseURL string) (string, error) {
	c := &Client{BaseURL: baseURL, HTTP: hc}
	var out struct{ ServerName string }
	err := c.getJSON(ctx, "/System/Info/Public", nil, &out)
	return strings.TrimSpace(out.ServerName), err
}

type Series struct {
	ID           string
	Name         string
	Year         int
	HasImage     bool
	Unplayed     int
	LastPlayedAt int64 // unix seconds, 0 if never
}

type item struct {
	Id                string
	Name              string
	ProductionYear    int
	IndexNumber       *int
	ParentIndexNumber *int
	ImageTags         map[string]string
	UserData          struct {
		UnplayedItemCount int
		LastPlayedDate    string
	}
}

type itemsResponse struct{ Items []item }

// ListSeries returns the TV series visible to the client's user. search and
// ids are optional filters; limit 0 means no limit.
func (c *Client) ListSeries(ctx context.Context, search string, ids []string, limit int) ([]Series, error) {
	q := url.Values{
		"userId":           {c.UserID},
		"IncludeItemTypes": {"Series"},
		"Recursive":        {"true"},
		"SortBy":           {"SortName"},
		"EnableUserData":   {"true"},
		"EnableImageTypes": {"Primary"},
	}
	if search != "" {
		q.Set("SearchTerm", search)
	}
	if len(ids) > 0 {
		q.Set("Ids", strings.Join(ids, ","))
	}
	if limit > 0 {
		q.Set("Limit", strconv.Itoa(limit))
	}
	var res itemsResponse
	if err := c.getJSON(ctx, "/Items", q, &res); err != nil {
		return nil, err
	}
	out := make([]Series, 0, len(res.Items))
	for _, it := range res.Items {
		s := Series{ID: it.Id, Name: it.Name, Year: it.ProductionYear, Unplayed: it.UserData.UnplayedItemCount}
		_, s.HasImage = it.ImageTags["Primary"]
		if t, err := time.Parse(time.RFC3339, it.UserData.LastPlayedDate); err == nil {
			s.LastPlayedAt = t.Unix()
		}
		out = append(out, s)
	}
	return out, nil
}

// NextUp describes the next episode to watch in a series, e.g.
// "S02E05 · The Title", or "" if Jellyfin has no suggestion.
func (c *Client) NextUp(ctx context.Context, seriesID string) (string, error) {
	q := url.Values{"userId": {c.UserID}, "seriesId": {seriesID}, "Limit": {"1"}}
	var res itemsResponse
	if err := c.getJSON(ctx, "/Shows/NextUp", q, &res); err != nil {
		return "", err
	}
	if len(res.Items) == 0 {
		return "", nil
	}
	ep := res.Items[0]
	if ep.ParentIndexNumber == nil || ep.IndexNumber == nil {
		return ep.Name, nil
	}
	label := fmt.Sprintf("S%02dE%02d", *ep.ParentIndexNumber, *ep.IndexNumber)
	if ep.Name != "" {
		label += " · " + ep.Name
	}
	return label, nil
}

// Image fetches an item's poster. The caller must close the response body.
func (c *Client) Image(ctx context.Context, itemID string) (*http.Response, error) {
	q := url.Values{"maxHeight": {"360"}, "quality": {"85"}}
	return c.do(ctx, http.MethodGet, "/Items/"+url.PathEscape(itemID)+"/Images/Primary", q, nil)
}
