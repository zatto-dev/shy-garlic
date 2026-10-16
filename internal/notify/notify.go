// Package notify sends a message when a service goes down or comes back.
//
// ! The webhook address is NOT in the configuration file. The config is the source of truth,
// we show it in the UI and commit it to git — a secret in such a file would leak
// on the first `git push`. So it lives in SQLite, next to passwords and sessions, and the
// panel sets it via PUT /api/notify.
//
// The environment variable (SHYG_WEBHOOK) still works and BEATS the database: an address given
// at startup belongs to the deploy, so the panel will not overwrite it. Otherwise
// a container restart would silently undo a change made in the UI.
//
// The message format (discord, slack, ntfy, gotify, telegram, generic) is
// stored next to the address, in SQLite; SHYG_WEBHOOK_FORMAT has the same
// precedence over the database as SHYG_WEBHOOK.
//
// Sending happens in the background, one queue per service (the event order of a single
// service is preserved), so a slow or dead webhook does not hold up
// saving the check history.
//
// We deliberately do NOT build alerting here. Uptime Kuma does it better and we do not
// intend to replace it. This is meant to be one sentence on a channel: "it went down" and
// "it came back", without escalation, silence schedules and rules.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Message formats (SHYG-50). The webhook address and the format live next to each other in
// SQLite; the format only says how to dress the message and where to pack it:
//
//	discord   embed with a color bar (the default, as before)
//	slack     {"text": …} — also Mattermost and Rocket.Chat
//	ntfy      plain text + Title/Priority/Tags/Click headers
//	gotify    {"title","message","priority"} on POST /message?token=…
//	telegram  {"text": …} on POST …/sendMessage?chat_id=… (bot token in the path)
//	generic   stable JSON, see genericMessage
type Format string

const (
	FormatDiscord  Format = "discord"
	FormatSlack    Format = "slack"
	FormatNtfy     Format = "ntfy"
	FormatGotify   Format = "gotify"
	FormatTelegram Format = "telegram"
	FormatGeneric  Format = "generic"
)

// Formats — the order from the dropdown in the UI.
var Formats = []Format{FormatDiscord, FormatSlack, FormatNtfy, FormatGotify, FormatTelegram, FormatGeneric}

func ValidFormat(s string) bool {
	for _, f := range Formats {
		if string(f) == s {
			return true
		}
	}
	return false
}

var (
	ErrTelegramAddress = errors.New("telegram: the address has to look like https://api.telegram.org/bot<TOKEN>/sendMessage?chat_id=<ID>")
	ErrGotifyAddress   = errors.New("gotify: the address has to look like https://<host>/message?token=<APP_TOKEN>")
)

// ValidateAddress guards the shape of the address required by a given format — so that
// a typo shows up on save, and not only as a 404 at three in the morning.
// It does not check the scheme (the caller does that), only the path and parameters.
func ValidateAddress(f Format, address string) error {
	if address == "" {
		return nil
	}
	u, err := url.Parse(address)
	if err != nil {
		return err
	}
	switch f {
	case FormatTelegram:
		if !strings.HasSuffix(u.Path, "/sendMessage") || u.Query().Get("chat_id") == "" {
			return ErrTelegramAddress
		}
	case FormatGotify:
		if !strings.HasSuffix(u.Path, "/message") || u.Query().Get("token") == "" {
			return ErrGotifyAddress
		}
	}
	return nil
}

// Colors of the bars in Discord embeds — the same as the states in the panel.
const (
	colorDown     = 0xE5484D
	colorDegraded = 0xE8A33D
	colorUp       = 0xA4CF36
)

type Event struct {
	Key   string
	Name  string
	Group string
	URL   string
	State string // up | degraded | down
	Code  int
	Err   string
	MS    int64
	At    time.Time

	// Previous — the last state we notified about (up | degraded | down).
	// Filled in by Observe; for the generic format.
	Previous string
	// Test — a test message from the button in settings.
	Test bool
}

// kind is the event name in the generic format: down | degraded | recovered | test.
func (z Event) kind() string {
	switch {
	case z.Test:
		return "test"
	case z.State == "up":
		return "recovered"
	default:
		return z.State
	}
}

type serviceState struct {
	reported   string // the last state we notified about
	failStreak int    // how many checks in a row were not "up"
	// pending — states whose message sits in the queue or is on its way.
	// Without it, subsequent checks (every dozen or so seconds), while sending is still
	// in progress, would queue the same alert over and over.
	pending map[string]bool
}

// sendJob — a single message to send. It holds a POINTER to the service state, not
// its key: Move re-points the same pointer under the new key, so the result
// of a send started before a rename lands in the right place.
type sendJob struct {
	z Event
	s *serviceState
}

// serviceQueue — messages of a single service, sent one by one by one
// goroutine, so that "down" never overtakes "back up".
type serviceQueue struct {
	events  []sendJob
	running bool
}

const (
	// maxServiceQueue — a hard limit; with `pending` the queue holds in practice
	// at most two items, this is a safety net against a bug.
	maxServiceQueue = 8
	// maxConcurrentSends — how many messages may be in flight at once. A network
	// outage takes down all services within one second, and Discord answers 429
	// after a few messages.
	maxConcurrentSends = 3
)

type Notifier struct {
	// cfgMu guards the webhook, format and threshold. The monitor loop reads them, an HTTP
	// request from the panel writes them — without it the race is certain, not hypothetical.
	cfgMu     sync.RWMutex
	webhook   string
	format    Format
	threshold int

	client *http.Client

	mu     sync.Mutex
	state  map[string]*serviceState
	queues map[string]*serviceQueue

	sem chan struct{}
	wg  sync.WaitGroup
}

// New returns a notifier. An empty webhook = everything keeps working, only nothing
// goes out.
func New(webhook string, threshold int) *Notifier {
	if threshold < 1 {
		threshold = 2
	}
	return &Notifier{
		webhook:   webhook,
		format:    FormatDiscord,
		threshold: threshold,
		client:    &http.Client{Timeout: 10 * time.Second},
		state:     map[string]*serviceState{},
		queues:    map[string]*serviceQueue{},
		sem:       make(chan struct{}, maxConcurrentSends),
	}
}

func (p *Notifier) Enabled() bool { return p.Webhook() != "" }

// SetWebhook replaces the address on the fly. An empty one disables notifications.
func (p *Notifier) SetWebhook(address string) {
	p.cfgMu.Lock()
	defer p.cfgMu.Unlock()
	p.webhook = address
}

// Webhook returns the current address.
func (p *Notifier) Webhook() string {
	p.cfgMu.RLock()
	defer p.cfgMu.RUnlock()
	return p.webhook
}

// SetFormat replaces the message format. An unknown one falls back to the default
// (discord) — a format from the database that this version does not know must not disable alerts.
func (p *Notifier) SetFormat(f Format) {
	if !ValidFormat(string(f)) {
		f = FormatDiscord
	}
	p.cfgMu.Lock()
	defer p.cfgMu.Unlock()
	p.format = f
}

// Format returns the current format.
func (p *Notifier) Format() Format {
	p.cfgMu.RLock()
	defer p.cfgMu.RUnlock()
	return p.format
}

// SetThreshold replaces the number of failed checks in a row. Below 1 makes no
// sense, so it is clamped to 1.
func (p *Notifier) SetThreshold(n int) {
	if n < 1 {
		n = 1
	}
	p.cfgMu.Lock()
	defer p.cfgMu.Unlock()
	p.threshold = n
}

// Threshold returns the current threshold.
func (p *Notifier) Threshold() int {
	p.cfgMu.RLock()
	defer p.cfgMu.RUnlock()
	return p.threshold
}

// Observe decides whether this event deserves a message and, if so,
// hands it off for sending in the background — the caller does not wait for the network.
//
// A failure must be confirmed (by default two checks in a row), so that a single
// network blip does not wake anyone at night. A recovery is reported immediately.
//
// ! The "reported" state is set only by the send, and only a successful one (see process).
// Earlier the state was saved before sending, so one failed attempt
// lost the "down" alert forever. With background sending the same guarantee is held by
// `pending`: a failed send removes it, and the next check retries.
func (p *Notifier) Observe(ctx context.Context, z Event) {
	if !p.Enabled() {
		return
	}
	job, ok := p.decide(z)
	if !ok {
		return
	}
	if !p.enqueue(ctx, job) {
		slog.Warn("service notification queue full, will retry on the next check", "service", z.Key)
		p.mu.Lock()
		delete(job.s.pending, z.State)
		p.mu.Unlock()
	}
}

// Wait blocks until all messages in the queues have been sent or
// dropped — for tests and shutdown.
func (p *Notifier) Wait() { p.wg.Wait() }

// decide updates the counters and says whether a message has to be sent. A second
// result of false = nothing to send (unknown, the same state, a message already in flight).
// The mutex is always released via defer — earlier the `default` branch returned with
// it still held.
func (p *Notifier) decide(z Event) (sendJob, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, exists := p.state[z.Key]
	if !exists {
		s = &serviceState{reported: "up"} // we assume good faith at startup
		p.state[z.Key] = s
	}
	var send bool
	switch z.State {
	case "up":
		s.failStreak = 0
		send = s.reported != "up"
	case "down", "degraded":
		s.failStreak++
		send = s.failStreak >= p.Threshold() && s.reported != z.State
	default: // unknown — we know nothing, we do not bother anyone
		return sendJob{}, false
	}
	if !send || s.pending[z.State] {
		return sendJob{}, false
	}
	if s.pending == nil {
		s.pending = map[string]bool{}
	}
	s.pending[z.State] = true
	z.Previous = s.reported
	return sendJob{z: z, s: s}, true
}

// enqueue puts a job in the service's queue and, if needed, wakes its
// goroutine. false = queue full.
func (p *Notifier) enqueue(ctx context.Context, job sendJob) bool {
	p.mu.Lock()
	k := p.queues[job.z.Key]
	if k == nil {
		k = &serviceQueue{}
		p.queues[job.z.Key] = k
	}
	if len(k.events) >= maxServiceQueue {
		p.mu.Unlock()
		return false
	}
	k.events = append(k.events, job)
	start := !k.running
	k.running = true
	if start {
		p.wg.Add(1)
	}
	p.mu.Unlock()
	if start {
		go p.process(ctx, job.z.Key)
	}
	return true
}

// process drains one service's queue in order and ends when it is empty.
func (p *Notifier) process(ctx context.Context, key string) {
	defer p.wg.Done()
	for {
		p.mu.Lock()
		k := p.queues[key]
		if k == nil || len(k.events) == 0 {
			if k != nil {
				k.running = false
				delete(p.queues, key)
			}
			p.mu.Unlock()
			return
		}
		job := k.events[0]
		k.events = k.events[1:]
		p.mu.Unlock()

		err := p.sendWithRetry(ctx, job.z)

		p.mu.Lock()
		delete(job.s.pending, job.z.State)
		if err == nil {
			job.s.reported = job.z.State
		}
		p.mu.Unlock()
		if err != nil {
			slog.Warn("notification failed, will retry on the next check",
				"service", job.z.Key, "err", err)
		}
	}
}

// retryDelays — pauses between consecutive send attempts (a variable so that the
// test does not wait for seconds). The first attempt goes immediately, so there are
// len(retryDelays)+1 attempts.
var retryDelays = []time.Duration{500 * time.Millisecond, 2 * time.Second}

func (p *Notifier) sendWithRetry(ctx context.Context, z Event) error {
	err := p.sendLimited(ctx, z)
	for _, d := range retryDelays {
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (anulowano: %v)", err, ctx.Err())
		case <-time.After(d):
		}
		err = p.sendLimited(ctx, z)
	}
	return err
}

// sendLimited takes one of the maxConcurrentSends slots for the duration of the
// request itself — not for the wait between retries.
func (p *Notifier) sendLimited(ctx context.Context, z Event) error {
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.send(ctx, z)
}

// Move re-points a service's state under a new key (old → new) when the service
// was renamed or moved to another group. Without it Forget deleted the
// counter and "reported", so an ongoing outage alerted again and the recovery
// was never reported.
func (p *Notifier) Move(pairs map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for oldKey, newKey := range pairs {
		if oldKey == newKey {
			continue
		}
		if s, ok := p.state[oldKey]; ok {
			p.state[newKey] = s
			delete(p.state, oldKey)
		}
	}
}

// Forget clears the state of a service that is no longer in the config.
func (p *Notifier) Forget(alive map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.state {
		if !alive[k] {
			delete(p.state, k)
		}
	}
}

// ── building the request ────────────────────────────────────────────────────

func (p *Notifier) send(ctx context.Context, z Event) error {
	req, err := buildRequest(ctx, p.Format(), p.Webhook(), z)
	if err != nil {
		return redact(err)
	}
	req.Header.Set("User-Agent", "ShyGarlic")

	resp, err := p.client.Do(req)
	if err != nil {
		return redact(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: %s", resp.Status)
	}
	return nil
}

// redact strips the address from an error. *url.Error from net/http carries the whole URL, and in it
// the Telegram bot token, the Gotify token or the Discord webhook path — the error
// ends up both in the log and in the API response on "send test".
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("webhook: %s: %w", ue.Op, ue.Err)
	}
	return err
}

func buildRequest(ctx context.Context, f Format, address string, z Event) (*http.Request, error) {
	title, content := messageText(z)
	switch f {
	case FormatSlack:
		return jsonPOST(ctx, address, map[string]any{"text": title + "\n" + content})
	case FormatNtfy:
		return ntfyPOST(ctx, address, z, title, content)
	case FormatGotify:
		body := map[string]any{"title": title, "message": content, "priority": gotifyPriority(z)}
		if clickable(z.URL) && !z.Test {
			body["extras"] = map[string]any{
				"client::notification": map[string]any{"click": map[string]any{"url": z.URL}},
			}
		}
		return jsonPOST(ctx, address, body)
	case FormatTelegram:
		// chat_id travels in the address (?chat_id=…), just like the bot token in the path;
		// no parse_mode, so that _ and * in a service name do not break Markdown
		return jsonPOST(ctx, address, map[string]any{"text": title + "\n" + content, "disable_web_page_preview": true})
	case FormatGeneric:
		return jsonPOST(ctx, address, genericMessage(z))
	default:
		return jsonPOST(ctx, address, discordMessage(z))
	}
}

func jsonPOST(ctx context.Context, address string, body any) (*http.Request, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func discordMessage(z Event) map[string]any {
	title, description := discordText(z)
	color := colorUp
	switch z.State {
	case "down":
		color = colorDown
	case "degraded":
		color = colorDegraded
	}
	return map[string]any{
		"username": "Shy Garlic",
		"embeds": []map[string]any{{
			"title":       title,
			"description": description,
			"color":       color,
			"url":         z.URL,
			"footer":      map[string]any{"text": z.Group},
			"timestamp":   z.At.UTC().Format(time.RFC3339),
		}},
	}
}

// genericMessage — a CONTRACT for others' integrations; field names do not change.
//
//	event     down | degraded | recovered | test
//	service   service name
//	group     group name
//	state     new state: up | degraded | down
//	previous  last reported state: up | degraded | down
//	reason    the reason (e.g. "connection refused", "HTTP 502"); empty for recovered
//	at        check time, RFC 3339 UTC
//	url       service address
func genericMessage(z Event) map[string]any {
	reason := ""
	if z.State != "up" {
		reason = failureReason(z)
	}
	if z.Test {
		reason = "test message"
	}
	return map[string]any{
		"event": z.kind(), "service": z.Name, "group": z.Group,
		"state": z.State, "previous": z.Previous, "reason": reason,
		"at": z.At.UTC().Format(time.RFC3339), "url": z.URL,
	}
}

func ntfyPOST(ctx context.Context, address string, z Event, title, content string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", textHeader(title))
	priority, tag := "default", "white_check_mark"
	switch z.State {
	case "down":
		priority, tag = "high", "rotating_light"
	case "degraded":
		tag = "warning"
	}
	if z.Test {
		tag = "bell"
	}
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tag)
	if clickable(z.URL) && !z.Test {
		req.Header.Set("Click", z.URL)
	}
	return req, nil
}

// textHeader prepares text for an HTTP header: with no line breaks
// (a service name from the file will not inject a second header) and with RFC 2047
// encoding for non-ASCII characters, which ntfy decodes.
func textHeader(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return mime.QEncoding.Encode("utf-8", s)
}

func clickable(address string) bool {
	u, err := url.Parse(address)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		!strings.ContainsAny(address, "\r\n")
}

func gotifyPriority(z Event) int {
	switch z.State {
	case "down":
		return 8
	case "degraded":
		return 5
	}
	return 3
}

// messageText — title and content without formatting, common to all
// formats except Discord (which has an emoji in the title and code in the description).
func messageText(z Event) (title, content string) {
	if z.Test {
		return "Shy Garlic: test notification", "If you can read this, notifications work."
	}
	switch z.State {
	case "down":
		return z.Name + " is down", z.URL + "\n" + failureReason(z)
	case "degraded":
		return z.Name + " answers, but not as expected", z.URL + "\n" + failureReason(z)
	default:
		return z.Name + " is back", fmt.Sprintf("%s\n%d ms", z.URL, z.MS)
	}
}

func discordText(z Event) (title, description string) {
	if z.Test {
		return "🔔 Shy Garlic: test notification", "If you can read this, notifications work."
	}
	switch z.State {
	case "down":
		return "🔴 " + z.Name + " is down", fmt.Sprintf("`%s`\n%s", z.URL, failureReason(z))
	case "degraded":
		return "🟠 " + z.Name + " answers, but not as expected", fmt.Sprintf("`%s`\n%s", z.URL, failureReason(z))
	default:
		return "🟢 " + z.Name + " is back", fmt.Sprintf("`%s`\n%d ms", z.URL, z.MS)
	}
}

func failureReason(z Event) string {
	switch {
	case z.Err != "":
		return z.Err
	case z.Code != 0:
		return fmt.Sprintf("HTTP %d", z.Code)
	default:
		return "no answer"
	}
}

// Test sends a test message — so the configuration can be checked without
// waiting for something to really go down. It goes immediately (no queue and no retries), because
// a human is waiting for the answer and wants to see the real error.
func (p *Notifier) Test(ctx context.Context) error {
	if !p.Enabled() {
		return fmt.Errorf("no webhook configured")
	}
	return p.send(ctx, Event{
		Name: "Shy Garlic", Group: "test", URL: "http://localhost",
		State: "up", Previous: "up", At: time.Now(), Test: true,
	})
}
