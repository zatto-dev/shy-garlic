package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/zatto-dev/shy-garlic/internal/config"
)

// ── HTTP layer ───────────────────────────────────────────────────────────────

// iconRef tells the template how to render a service icon.
//
// When an icon has separate variants for the dark/light theme, Src stays empty and
// the template renders BOTH images at once (WariantCiemny/WariantJasny) — which one
// shows is decided by pure CSS on the data-tone attribute on <html>. The theme is a
// choice of the BROWSER (it can be switched without reloading the page, see
// appearance.tone in the config), so the server does not know which to show when
// rendering the page — it must give both and leave the decision to CSS, with zero
// extra JS or requests when the theme changes.
type iconRef struct {
	Src          string // path on our server, empty when missing or when there are variants
	Letter       string // fallback: a tile with a letter
	VariantDark  string // path to the variant for the DARK theme (light ink), empty = none
	VariantLight string // path to the variant for the LIGHT theme (dark ink), empty = none
}

func (s *Server) resolveIcon(icon, name string) iconRef {
	letter := firstLetter(name)
	switch {
	case icon == "":
		return iconRef{Letter: letter}
	case strings.HasPrefix(icon, "http://"), strings.HasPrefix(icon, "https://"):
		return iconRef{Src: "/icon/proxy?u=" + urlEscape(icon), Letter: letter}
	default:
		n := strings.TrimSuffix(strings.TrimSuffix(icon, ".svg"), ".png")
		if dark, light := s.icons.Variants(n); dark != "" && light != "" {
			return iconRef{VariantDark: "/icon/" + dark, VariantLight: "/icon/" + light, Letter: letter}
		}
		// even when the index did not load, try — the worst case is a 404
		// and the card shows a letter
		return iconRef{Src: "/icon/" + n, Letter: letter}
	}
}

// configIcons lists the catalog icon names the config uses.
func configIcons(cfg *config.Config) []string {
	var out []string
	for _, g := range cfg.Groups {
		for _, svc := range g.Services {
			n := strings.TrimSuffix(strings.TrimSuffix(svc.Icon, ".svg"), ".png")
			if n != "" && !strings.Contains(n, "://") {
				out = append(out, n)
			}
		}
	}
	return out
}

func urlEscape(s string) string {
	r := strings.NewReplacer("&", "%26", "?", "%3F", " ", "%20", "#", "%23")
	return r.Replace(s)
}

func (s *Server) handleIcon(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "proxy" {
		s.handleIconProxy(w, r)
		return
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".svg"), ".png")

	data, kind, err := s.icons.Read(r.Context(), name)
	if err != nil {
		// not on disk yet (the download may still be running) or missing: the card
		// shows its letter now, and the next render asks again
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	// an icon can be SVG, i.e. a document from our origin — and script-src has
	// 'unsafe-inline'. The sandbox closes this without taking away the icons' ability to work.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// an icon under a given name is immutable, so a week in the cache is
	// fine — unlike /bg/, where the file is sometimes replaced. Without full
	// indexes the name may still move to the other catalog: five minutes.
	if s.icons.Authoritative() {
		w.Header().Set("Cache-Control", "public, max-age=604800")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	_, _ = w.Write(data)
}

// iconKinds — the only thing allowed to be passed on from a foreign server.
var iconKinds = map[string]bool{
	"image/svg+xml": true, "image/png": true, "image/jpeg": true,
	"image/webp": true, "image/gif": true, "image/x-icon": true,
	"image/vnd.microsoft.icon": true,
}

// externalIconClient fetches icons from outside the panel.
//
// ! The block on private addresses sits in Dialer.Control, i.e. it WORKS ON THE
// ADDRESS THAT WAS ACTUALLY CHOSEN — checking the host before the request is
// bypassed with DNS that returns a public address once and a local one the next time.
// Without this, `icon: http://192.168.0.1/…` in the YAML turns the panel into a
// home network scanner.
func externalIconClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: blockLocalAddresses}
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

func blockLocalAddresses(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("unrecognized address %q", host)
	}
	if !publicAddress(ip) {
		return fmt.Errorf("local address %s is blocked", ip)
	}
	return nil
}

// notPublic lists ranges that look routable but are not the open internet — or
// that tunnel to an address the filter below would block if it saw it directly.
// The stdlib helpers (IsPrivate, IsLoopback, …) do not know them.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"; 0.x.x.x connects to the local host on Linux
	netip.MustParsePrefix("100.64.0.0/10"),  // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking, used for fake-IP DNS by some VPNs
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, incl. 255.255.255.255
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64: embeds an IPv4 address (10.0.0.1 → 64:ff9b::a00:1)
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2002::/16"),      // 6to4: embeds an IPv4 address
	netip.MustParsePrefix("2001::/32"),      // Teredo: embeds an IPv4 address
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsUnspecified(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// handleIconProxy handles icons given as a full address — the server fetches
// them, not the browser.
func (s *Server) handleIconProxy(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("u")
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		plainError(w, r, http.StatusBadRequest, "error_icon_url")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, src, nil)
	if err != nil {
		plainError(w, r, http.StatusBadRequest, "error_icon_url")
		return
	}
	req.Header.Set("User-Agent", "ShyGarlic/"+Version)
	client := s.proxyClient
	if client == nil {
		client = externalIconClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("icon proxy: fetch failed", "err", err)
		plainError(w, r, http.StatusBadGateway, "error_icon_upstream")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("icon proxy: upstream refused", "status", resp.Status)
		plainError(w, r, http.StatusBadGateway, "error_icon_upstream")
		return
	}
	// ! The Content-Type from the upstream used to be copied here VERBATIM, so
	// `icon: https://bad/x` returned text/html from OUR origin — i.e. XSS
	// with script-src 'unsafe-inline'. Now only the allowlist passes.
	kind, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !iconKinds[kind] {
		plainError(w, r, http.StatusBadGateway, "error_icon_upstream")
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		slog.Warn("icon proxy: read failed", "err", err)
		plainError(w, r, http.StatusBadGateway, "error_icon_upstream")
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(data)
}
