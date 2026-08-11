package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/hostcfg"
)

// ── terminal presentation ─────────────────────────────────────────────────────

// colorize reports whether stdout is an interactive terminal that wants ANSI.
// Honours NO_COLOR (https://no-color.org) and a plain `TERM=dumb`.
var colorize = func() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}()

const (
	cReset  = "\033[0m"
	cDim    = "\033[2m"
	cBold   = "\033[1m"
	cGreen  = "\033[32m"
	cRed    = "\033[31m"
	cAmber  = "\033[33m"
	cBlue   = "\033[34m"
	cCyan   = "\033[36m"
	cViolet = "\033[35m"

	// Bright (aixterm) variants. A terminal's normal green is a body-text
	// colour; a live indicator competing with a wall of it needs the bright
	// one to read as a signal rather than as more text.
	cBrightGreen = "\033[92m"
	cBrightAmber = "\033[93m"
)

func paint(s string, codes ...string) string {
	if !colorize || s == "" {
		return s
	}
	return strings.Join(codes, "") + s + cReset
}

// termWidth is the usable output width for prose-shaped output, clamped at 120
// because a wrapped paragraph 200 columns wide is unreadable.
func termWidth() int {
	if w := termWidthRaw(); w > 120 {
		return 120
	} else {
		return w
	}
}

// termWidthRaw is the terminal's true width, used by the listings — a table
// wants every column the terminal actually has.
func termWidthRaw() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 40 {
		return w
	}
	return 100
}

// fit renders s into exactly n display columns: one line, ellipsised when long,
// right-padded when short. clipLine's own bound is n runes PLUS the ellipsis,
// hence the n-1.
func fit(s string, n int) string {
	if n < 1 {
		return ""
	}
	s = clipLine(s, n)
	if len([]rune(s)) > n { // clipLine appended an ellipsis: n runes + "…"
		s = clipLine(s, n-1)
	}
	return pad(s, n)
}

// pad right-pads to n runes (no-op if already longer).
func pad(s string, n int) string {
	if d := n - len([]rune(s)); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// humanDur renders a duration the way a status line wants it: 45s, 12m, 3h04m, 2d3h.
func humanDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// humanBytes renders a byte count as B/KB/MB/GB.
func humanBytes(n int64) string {
	const k = 1024
	switch {
	case n < k:
		return fmt.Sprintf("%dB", n)
	case n < k*k:
		return fmt.Sprintf("%.0fKB", float64(n)/k)
	case n < k*k*k:
		return fmt.Sprintf("%.1fMB", float64(n)/(k*k))
	default:
		return fmt.Sprintf("%.1fGB", float64(n)/(k*k*k))
	}
}

// commas groups an integer with thousand separators (1204 → 1,204).
func commas(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + commas(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// short is the 8-char session handle shown in listings and accepted by every
// command that takes an <id>.
func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// sessionURL is the shareable web link for a session.
func sessionURL(id string) string { return hostcfg.WebURL() + "/session/" + id }

// age is how long a session has been running, or how long it ran.
func age(s api.Session) time.Duration {
	end := time.Now()
	if !s.EndedAt.IsZero() {
		end = s.EndedAt
	}
	if s.CreatedAt.IsZero() {
		return 0
	}
	return end.Sub(s.CreatedAt)
}

// statusBadge is the leading "● LIVE" / "○ ended" marker.
func statusBadge(s api.Session) string {
	if s.Live() {
		return paint("● LIVE ", cGreen, cBold)
	}
	if s.EndedReason == "quota" {
		return paint("○ quota", cRed)
	}
	return paint("○ ended", cDim)
}

// plural renders "1 event" / "2 events" — a listing full of "1 views" reads
// like a bug even when the number is right.
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return commas(n) + " " + word + "s"
}

// facts is the middle line of a session block: the numbers worth seeing.
func facts(s api.Session) []string {
	var f []string
	f = append(f, humanDur(age(s)))
	f = append(f, plural(s.EventCount, "event"))
	if s.Live() && s.WatcherCount > 0 {
		f = append(f, fmt.Sprintf("%d watching", s.WatcherCount))
	} else if s.ViewCount > 0 {
		f = append(f, plural(s.ViewCount, "view"))
	}
	if s.ForkCount > 0 {
		f = append(f, plural(s.ForkCount, "fork"))
	}
	if s.ShareCount > 0 {
		f = append(f, plural(s.ShareCount, "share"))
	}
	if s.BytesUsed > 0 {
		f = append(f, humanBytes(s.BytesUsed))
	}
	if s.OutputTokens > 0 {
		f = append(f, fmt.Sprintf("%s tok out", commas(int(s.OutputTokens))))
	}
	if s.GitBranch != "" {
		f = append(f, s.GitBranch+"@"+repoName(s.GitRemote))
	}
	if s.SharedRole != "" {
		f = append(f, "shared to you as "+s.SharedRole)
	}
	if s.Visibility == "public" || s.Visibility == "link" || s.Visibility == "open" {
		f = append(f, s.Visibility)
	}
	return f
}

// repoName reduces a git remote to owner/repo for display.
func repoName(remote string) string {
	if remote == "" {
		return "local"
	}
	r := strings.TrimSuffix(strings.TrimSuffix(remote, "/"), ".git")
	// scp-style (git@host:owner/repo) and URL forms both reduce to their last
	// two path segments once ":" is treated as a separator.
	var parts []string
	for _, p := range strings.Split(strings.ReplaceAll(r, ":", "/"), "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return r
}

// printSession writes one three-line block: status + title + agent, the facts
// line, and the handle + link line. Used where a single session is the subject
// (info, the join header) and the detail is the point; listings use
// printSessionRow instead.
func printSession(s api.Session) {
	w := termWidth()
	agent := agentOf(s)
	right := strings.TrimSpace(agent + "  " + s.Model)
	col := max(12, w-14-len([]rune(right)))
	fmt.Printf(" %s  %s  %s\n",
		statusBadge(s),
		paint(fit(s.Title, col), cBold),
		paint(right, cCyan))
	fmt.Printf("   %s\n", paint(clipLine(strings.Join(facts(s), " · "), w-4), cDim))
	fmt.Printf("   %s  %s\n",
		paint(short(s.ID), cViolet),
		paint(clipLine(sessionURL(s.ID), w-16), cDim))
}

// agentOf is the agent label for a session, falling back to the framework.
func agentOf(s api.Session) string {
	if s.Agent != "" {
		return s.Agent
	}
	return s.Framework
}

// listLayout is the column plan for a listing, sized once for the whole table so
// every row aligns. The link is never truncated — a half URL is useless — so the
// title column absorbs a narrow terminal, and the agent column is dropped
// entirely before anything that identifies the session gets cut.
type listLayout struct {
	name  int
	link  int
	agent int
}

// minName is the narrowest title column that still names a session.
const minName = 20

func planList(rows []api.Session, width int) listLayout {
	link, agent, name := 0, 0, 0
	for _, s := range rows {
		link = max(link, len(sessionURL(s.ID)))
		agent = max(agent, len([]rune(agentOf(s))))
		name = max(name, len([]rune(s.Title)))
	}
	// Natural widths, capped. Columns are never STRETCHED to fill the terminal —
	// a wide screen would otherwise push the ids and links out to the far right
	// with a lake of padding in between, which is exactly what the eye has to
	// cross to read a row.
	l := listLayout{name: min(name, 32), link: link, agent: min(agent, 14)}
	// " " + status(7) + "  " + name + "  " + id(8) + "  " + link + "  " + agent
	fixed := func(l listLayout) int { return 1 + 7 + 2 + 2 + 8 + 2 + l.link + 2 + l.agent }
	if fixed(l)+l.name > width { // too narrow: drop the agent before squeezing the title
		l.agent = 0
	}
	if fixed(l)+l.name > width {
		// Below minName the title stops identifying anything, so the row is
		// allowed to run past the edge and soft-wrap instead. The link is what
		// the row is for, and a clipped URL is useless.
		l.name = max(minName, width-fixed(l))
	}
	return l
}

// printSessionRow writes one session as a single line:
// status, title, handle, link, agent.
func printSessionRow(s api.Session, l listLayout) {
	line := fmt.Sprintf(" %s  %s  %s  %s",
		statusBadge(s),
		paint(fit(s.Title, l.name), cBold),
		paint(short(s.ID), cViolet),
		paint(sessionURL(s.ID), cDim))
	if l.agent > 0 {
		line += "  " + paint(agentOf(s), cCyan)
	}
	fmt.Println(line)
}

// ── live ls ───────────────────────────────────────────────────────────────────

// cmdLs lists the caller's sessions — owned, shared with them, or both.
func cmdLs(args []string, defScope string) int {
	// A listing is a glance, not an archive: ten rows is what fits on screen
	// above the prompt. --all lifts the cap and pages through the rest.
	const defaultLimit = 10
	opts := api.ListOpts{Scope: defScope, Status: "all", Limit: defaultLimit}
	asJSON, wantAll := false, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--mine":
			opts.Scope = "mine"
		case "--shared":
			opts.Scope = "shared"
		case "--all":
			wantAll = true
		case "--live":
			opts.Status = "live"
		case "--ended":
			opts.Status = "ended"
		case "--json":
			asJSON = true
		case "-q", "--query":
			if i+1 < len(args) {
				i++
				opts.Query = args[i]
			}
		case "-n", "--limit":
			if i+1 < len(args) {
				i++
				opts.Limit, _ = strconv.Atoi(args[i])
			}
		default:
			fmt.Fprintf(os.Stderr, "live ls: unknown option %q\n", a)
			return 2
		}
	}

	c := api.New()
	if wantAll {
		opts.Limit = 0 // fetch every page
	}
	rows, total, err := listAll(c, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"results": rows, "total": total})
		return 0
	}

	if len(rows) == 0 {
		fmt.Println(paint("no sessions"+scopeSuffix(opts), cDim))
		return 0
	}

	live := 0
	for _, s := range rows {
		if s.Live() {
			live++
		}
	}
	count := fmt.Sprintf("%d of %d", len(rows), total)
	if len(rows) == total {
		count = plural(total, "session")
	}
	fmt.Printf("\n%s  %s\n\n", paint(strings.ToUpper(scopeLabel(opts.Scope)), cBold),
		paint(fmt.Sprintf("%d live · %s", live, count), cDim))

	layout := planList(rows, termWidthRaw())
	for _, s := range rows {
		printSessionRow(s, layout)
	}
	fmt.Println()
	if total > len(rows) {
		fmt.Println(paint(fmt.Sprintf("  %d more — live ls --all", total-len(rows)), cDim))
	}
	if live > 0 {
		fmt.Println(paint("  join a live one:  live join <id>", cDim))
	}
	return 0
}

// listAll fetches one page, or every page when Limit is 0. The server caps a
// page at 100 rows, so "all" is a loop, not a bigger number.
func listAll(c *api.Client, o api.ListOpts) ([]api.Session, int, error) {
	if o.Limit > 0 {
		return c.ListSessions(o)
	}
	const page = 100
	o.Limit = page
	var all []api.Session
	for {
		rows, total, err := c.ListSessions(o)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, rows...)
		if len(rows) < page || len(all) >= total {
			return all, total, nil
		}
		o.Offset = len(all)
	}
}

func scopeLabel(scope string) string {
	switch scope {
	case "mine":
		return "my sessions"
	case "shared":
		return "shared with me"
	default:
		return "sessions"
	}
}

func scopeSuffix(o api.ListOpts) string {
	s := ""
	if o.Scope == "shared" {
		s = " shared with you"
	}
	if o.Status == "live" {
		s += " running right now"
	}
	if o.Query != "" {
		s += " matching " + strconv.Quote(o.Query)
	}
	return s
}

// ── live info ─────────────────────────────────────────────────────────────────

// cmdInfo prints everything known about one session, plus its last few events.
func cmdInfo(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: live info <session-id>")
		return 2
	}
	c := api.New()
	id, err := resolveID(c, args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	s, err := c.GetSession(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	if len(args) > 1 && args[1] == "--json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(s)
		return 0
	}

	fmt.Println()
	printSession(s)
	kv := [][2]string{
		{"id", s.ID},
		{"owner", s.OwnerHandle},
		{"agent", strings.TrimSpace(s.Agent + " " + s.CaptureMode)},
		{"model", s.Model},
		{"started", localTS(s.CreatedAt)},
	}
	if !s.EndedAt.IsZero() {
		kv = append(kv, [2]string{"ended", localTS(s.EndedAt)})
	}
	if s.ClientHandle != "" {
		kv = append(kv, [2]string{"machine", s.ClientHandle})
	}
	if s.GitRemote != "" {
		kv = append(kv, [2]string{"repo", repoName(s.GitRemote) + " @ " + s.GitBranch})
	}
	if s.Live() {
		kv = append(kv, [2]string{"agent stream", yesNo(s.AgentConnected)})
	}
	if s.ForkedFrom != nil {
		kv = append(kv, [2]string{"forked from",
			fmt.Sprintf("%s (%s) by %s @seq %d", short(s.ForkedFrom.ID), clipLine(s.ForkedFrom.Title, 40),
				s.ForkedFrom.OwnerHandle, s.ForkedFrom.Seq)})
	}
	kv = append(kv, [2]string{"visibility", s.Visibility}, [2]string{"link", sessionURL(s.ID)})
	fmt.Println()
	for _, p := range kv {
		if p[1] == "" {
			continue
		}
		fmt.Printf("   %s %s\n", paint(pad(p[0], 14), cDim), p[1])
	}

	if shown := visibleEvents(s.Events); len(shown) > 0 {
		n, tail := len(shown), shown
		if n > 10 {
			tail = tail[n-10:]
		}
		w := termWidth()
		fmt.Printf("\n%s\n", paint(strings.Repeat("─", w), cDim))
		fmt.Printf(" %s\n\n", paint(fmt.Sprintf("last %d of %s", len(tail), plural(n, "event")), cDim))
		printBlocks(tail, s, signedInName(), w)
	}
	return 0
}

func yesNo(b bool) string {
	if b {
		return "connected"
	}
	return "not connected"
}

func localTS(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// visibleEvents drops the internal stream-boundary events, which carry no
// content and only pad a listing.
func visibleEvents(all []api.Event) []api.Event {
	out := make([]api.Event, 0, len(all))
	for _, e := range all {
		if !hiddenEvents[e.EventType] {
			out = append(out, e)
		}
	}
	return out
}

// ── live stop / open / share / hosts ──────────────────────────────────────────

// cmdStopSession ends someone else's — or your own — running session remotely.
func cmdStopSession(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: live stop <session-id>")
		return 2
	}
	c := api.New()
	id, err := resolveID(c, args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	url, err := c.Stop(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "live: could not stop session:", err)
		return 1
	}
	fmt.Println("✓ stopped —", url)
	return 0
}

// cmdOpen opens a session's web page in the default browser.
func cmdOpen(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: live open <session-id>")
		return 2
	}
	id, err := resolveID(api.New(), args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	u := sessionURL(id)
	fmt.Println(u)
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if err := exec.Command(opener, u).Start(); err != nil {
		return 0 // the URL is printed; not opening a browser is not a failure
	}
	return 0
}

// cmdShare grants another account access to a session. "commenter" is the role
// a CLI joiner needs to talk back into the session; "viewer" is watch-only.
func cmdShare(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: live share <session-id> <email> [--role viewer|commenter]")
		fmt.Fprintln(os.Stderr, "       live share <session-id> --list")
		return 2
	}
	c := api.New()
	id, err := resolveID(c, args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	if args[1] == "--list" {
		shares, err := c.ListShares(id)
		if err != nil {
			fmt.Fprintln(os.Stderr, "live:", err)
			return 1
		}
		if len(shares) == 0 {
			fmt.Println(paint("no grants on this session", cDim))
			return 0
		}
		for _, s := range shares {
			fmt.Printf("  %s %s\n", paint(pad(s.Role, 10), cCyan), s.Email)
		}
		return 0
	}

	email, role := args[1], "commenter"
	for i := 2; i < len(args); i++ {
		if args[i] == "--role" && i+1 < len(args) {
			i++
			role = args[i]
		}
	}
	if role != "viewer" && role != "commenter" {
		fmt.Fprintln(os.Stderr, "live: role must be viewer or commenter")
		return 2
	}
	if err := c.CreateShare(id, email, role); err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	fmt.Printf("✓ shared with %s as %s\n", email, role)
	fmt.Printf("  they can join from their terminal:  %s\n", paint("live join "+short(id), cBold))
	return 0
}

// cmdHosts lists the caller's machines currently running `live daemon`.
func cmdHosts(args []string) int {
	hosts, err := api.New().ListHosts()
	if err != nil {
		fmt.Fprintln(os.Stderr, "live:", err)
		return 1
	}
	if len(hosts) == 0 {
		fmt.Println(paint("no machines online — run `live daemon` on one", cDim))
		return 0
	}
	fmt.Printf("\n%s\n\n", paint("ONLINE MACHINES", cBold))
	for _, h := range hosts {
		name := h.Name
		if name == "" {
			name = h.Hostname
		}
		fmt.Printf(" %s %s  %s\n", paint("●", cGreen), paint(name, cBold),
			paint(h.OS+"/"+h.Arch, cCyan))
		fmt.Printf("   %s\n", paint(strings.Join(h.Agents, ", "), cDim))
		for _, d := range h.Dirs {
			fmt.Printf("   %s\n", paint(d, cDim))
		}
		fmt.Println()
	}
	return 0
}

// ── id resolution ─────────────────────────────────────────────────────────────

// resolveID accepts a full session uuid or the 8-char handle printed by
// `live ls`, resolving the latter against the caller's own + shared sessions.
func resolveID(c *api.Client, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("no session id given")
	}
	// A pasted session link — checked before the length test, since a URL is
	// always longer than a bare uuid and would otherwise pass straight through.
	if u := strings.TrimSuffix(ref, "/"); strings.Contains(u, "/session/") {
		return u[strings.LastIndex(u, "/session/")+len("/session/"):], nil
	}
	// A full uuid (or anything already long enough) goes straight through.
	if len(ref) >= 36 {
		return ref, nil
	}
	rows, _, err := c.ListSessions(api.ListOpts{Scope: "all", Status: "all", Limit: 100})
	if err != nil {
		return "", err
	}
	var hits []api.Session
	for _, s := range rows {
		if strings.HasPrefix(s.ID, ref) {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].ID, nil
	case 0:
		return "", fmt.Errorf("no session of yours starts with %q — try the full id, or `live ls`", ref)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches %d sessions:", ref, len(hits))
		for _, s := range hits {
			fmt.Fprintf(&b, "\n  %s  %s", short(s.ID), clipLine(s.Title, 50))
		}
		return "", fmt.Errorf("%s", b.String())
	}
}
