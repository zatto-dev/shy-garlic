// Package dockerdisc suggests services based on the containers running
// on the host.
//
// Mind the boundary: we do not manage Docker and do not intend to. We read the
// container list, propose ready-made entries, and the user accepts them — only
// then do they land in their file. The file stays the source of truth; the panel
// does not start generating it behind the user's back.
//
// The Docker socket is mounted read-only and only if someone wants it.
// Without the socket the feature simply does not exist — no errors.
package dockerdisc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultSocket — the standard path of the Docker socket.
const DefaultSocket = "/var/run/docker.sock"

// Prefix of the labels with which a container can tell how it should look in the panel.
const labelPrefix = "shyg."

type Client struct {
	socket  string // unix socket path (local mode)
	baseURL string // HTTP base address (proxy mode)
	http    *http.Client
}

// New takes either a socket path or an http(s) address — the latter variant
// is for socket-proxy.
//
// ! A Docker socket mounted straight into the container with a panel exposed to
// the internet gives, in practice, power over the whole host if someone finds a
// hole in the panel. A proxy limited to reading the container list
// (e.g. tecnativa/docker-socket-proxy with CONTAINERS=1) closes that problem, so
// we support both variants and recommend the second one in the documentation.
func New(address string) *Client {
	if strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://") ||
		strings.HasPrefix(address, "tcp://") {
		baseURL := strings.Replace(address, "tcp://", "http://", 1)
		return &Client{
			baseURL: strings.TrimRight(baseURL, "/"),
			http:    &http.Client{Timeout: 8 * time.Second},
		}
	}
	if address == "" {
		address = DefaultSocket
	}
	return &Client{
		socket: address,
		http: &http.Client{
			Timeout: 8 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", address)
				},
			},
		},
	}
}

// Available tells whether we have anything to look into.
func (k *Client) Available() bool {
	if k.baseURL != "" {
		return true // we check the proxy only when asked — it may come up more slowly
	}
	fi, err := os.Stat(k.socket)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

func (k *Client) listURL() string {
	if k.baseURL != "" {
		return k.baseURL + "/containers/json"
	}
	return "http://docker/containers/json"
}

// Candidate is a proposed config entry, not yet anyone's.
type Candidate struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Icon  string `json:"icon"`
	Group string `json:"group,omitempty"`
	Image string `json:"image"`
	Ports []int  `json:"ports"`
	// Reason is a code, not a sentence: the UI translates it, because this package
	// has no right to decide the panel's language.
	Reason string `json:"why"` // label | port | noport
}

type container struct {
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

// Candidates reads the containers and turns them into proposals. `host` is the
// host name under which the user views the panel — we assume containers on the
// same machine are under the same address.
func (k *Client) Candidates(ctx context.Context, host string) ([]Candidate, error) {
	if !k.Available() {
		return nil, fmt.Errorf("brak gniazda Dockera (%s)", k.socket)
	}
	// ! no version number in the path. The daemon rejects versions older than its
	// MinAPIVersion (Docker 29 requires ≥1.44), and a path without a version always
	// hits that daemon's default version.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.listURL(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker: %s", resp.Status)
	}

	var list []container
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}

	var out []Candidate
	for _, c := range list {
		if c.State != "running" {
			continue
		}
		if c.Labels[labelPrefix+"enable"] == "false" {
			continue
		}
		if candidate, ok := fromContainer(c, host); ok {
			out = append(out, candidate)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func fromContainer(c container, host string) (Candidate, bool) {
	name := prettyName(c)
	if v := c.Labels[labelPrefix+"name"]; v != "" {
		name = v
	}

	// ports published on the host — only those make sense as an address
	var ports []int
	for _, p := range c.Ports {
		if p.Type == "tcp" && p.PublicPort > 0 {
			ports = append(ports, p.PublicPort)
		}
	}
	sort.Ints(ports)
	ports = dedupe(ports)

	k := Candidate{
		Name:  name,
		Image: stripTag(c.Image),
		Icon:  guessIcon(c),
		Group: c.Labels[labelPrefix+"group"],
		Ports: ports,
	}

	switch {
	case c.Labels[labelPrefix+"url"] != "":
		k.URL = c.Labels[labelPrefix+"url"]
		k.Reason = "label"
	case len(ports) > 0:
		k.URL = fmt.Sprintf("http://%s:%d", host, bestPort(ports))
		k.Reason = "port"
	default:
		// a container without a host port — we do not guess the address, but we show
		// it, because the user may have their own reverse proxy
		k.Reason = "noport"
	}
	return k, true
}

// bestPort picks the one that is most likely the web interface.
func bestPort(ports []int) int {
	popular := map[int]int{80: 100, 8080: 90, 443: 85, 3000: 80, 8000: 75, 8096: 70, 9000: 65}
	best, points := ports[0], -1
	for _, p := range ports {
		w := popular[p]
		if w == 0 {
			// high ephemeral ports are less likely than low ones
			w = 50 - p/2000
		}
		if w > points {
			best, points = p, w
		}
	}
	return best
}

func prettyName(c container) string {
	n := "kontener"
	if len(c.Names) > 0 {
		n = strings.TrimPrefix(c.Names[0], "/")
	}
	// "media-jellyfin-1" → "Jellyfin": cut the stack prefix and the replica suffix
	parts := strings.Split(n, "-")
	if len(parts) > 1 {
		if _, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			parts = parts[:len(parts)-1]
		}
	}
	if len(parts) > 1 {
		n = parts[len(parts)-1]
	} else {
		n = parts[0]
	}
	if n == "" {
		return "kontener"
	}
	return strings.ToUpper(n[:1]) + n[1:]
}

func stripTag(image string) string {
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image
}

// guessIcon takes the last segment of the image name — "ghcr.io/gethomepage/homepage"
// gives "homepage", which usually hits a name from the icon catalog.
func guessIcon(c container) string {
	if v := c.Labels[labelPrefix+"icon"]; v != "" {
		return v
	}
	image := stripTag(c.Image)
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	return strings.ToLower(image)
}

func dedupe(x []int) []int {
	if len(x) < 2 {
		return x
	}
	out := x[:1]
	for _, v := range x[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
