package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/liveshortly/live/internal/api"
	"github.com/liveshortly/live/internal/auth"
)

// cmdJoin attaches this terminal to another (or your own) live session: the
// session's events stream in, and anything you type is delivered to the running
// agent as a viewer message — the same channel the web composer uses. That
// makes it a CLI↔CLI conversation between two people through one agent.
//
// `live watch` is the same thing with the input half turned off.
func cmdJoin(args []string, readOnly bool) int {
	verb := "join"
	if readOnly {
		verb = "watch"
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: live %s <session-id>\n", verb)
		fmt.Fprintln(os.Stderr, "       live ls --live      # find one")
		return 2
	}
	history := 20
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--read-only", "--watch":
			readOnly = true
		case "--full":
			history = 1 << 30
		case "-n", "--history":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "live join: -n requires a count")
				return 2
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				fmt.Fprintf(os.Stderr, "live join: invalid history count %q\n", args[i])
				return 2
			}
			history = n
		}
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
	me := signedInName()

	w := termWidth()
	printSessionHeader(s, w)

	// Prior context: the events we already hold. Everything at or below this seq
	// is replay, so the stream's catch-up frames get dropped instead of printed
	// twice.
	baseSeq := 0
	shown := visibleEvents(s.Events)
	for _, e := range shown {
		if e.Seq > baseSeq {
			baseSeq = e.Seq
		}
	}
	if len(shown) > history {
		fmt.Printf("%s\n\n", paint(fmt.Sprintf("   ⋮ %d earlier events hidden — live %s %s --full for all of them",
			len(shown)-history, verb, short(id)), cDim))
		shown = shown[len(shown)-history:]
	}
	if len(shown) == 0 {
		fmt.Printf(" %s\n\n", paint("nothing captured yet — this session has not produced an event", cDim))
	}
	printBlocks(shown, s, me, w)

	if !s.Live() {
		fmt.Println(paint(strings.Repeat("─", w), cDim))
		fmt.Printf(" %s\n", paint("this session has ended — nothing more will arrive", cDim))
		fmt.Printf(" %s\n\n", paint("continue it as your own:  live claude --session "+short(id), cDim))
		return 0
	}

	fmt.Println(paint(strings.Repeat("─", w), cDim))
	if readOnly {
		fmt.Printf(" %s\n\n", paint("watching — Ctrl-C to leave", cDim))
	} else {
		fmt.Printf(" %s\n\n", paint("joined — type to talk to the agent · /help for commands · Ctrl-C to leave", cDim))
		if pendingPermission(s.Events) {
			fmt.Printf(" %s\n\n", paint("⏸ this session is waiting on a permission — /allow, /deny, or /always", cAmber, cBold))
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; cancel() }()

	// Live status for the prompt chip: watchers come from the stream's own
	// heartbeat frames, uptime from the session's start.
	var watchers atomic.Int64
	watchers.Store(int64(s.WatcherCount))
	// blocked is set while the agent is waiting on a permission nobody has
	// answered. It drives the prompt chip, so a stalled session is visible even
	// when the request has scrolled away. Seeded from the history: joining a
	// session that is ALREADY stuck is the case this exists for.
	var blocked atomic.Bool
	blocked.Store(pendingPermission(s.Events))

	// workingUntil is the instant the "is working" indicator goes quiet. The two
	// captures need different rules, because they emit different things:
	//
	//   hooks (claude)  brackets a turn — prompt … response — and emits NOTHING
	//                   while the model thinks. Silence cannot clear the
	//                   indicator here or it dies a few seconds into every turn,
	//                   which is exactly the stretch a watcher wants it for. So
	//                   an opening event holds it until the turn closes.
	//   pty  (ollama…)  has no prompt/response at all, only `output` and a
	//                   `stream_start` heartbeat every 2s while output buffers.
	//                   Silence is the only signal available, and the window has
	//                   to exceed the flush interval or it strobes between the
	//                   batches of one reply.
	//
	// Taking the later of the two deadlines covers both without branching on the
	// capture mode: a pty session never emits an opening event, and a heartbeat
	// mid-turn can never cut a turn short.
	var workingUntil atomic.Int64
	label := agentLabel(s)
	started := s.CreatedAt
	status := func() string {
		if blocked.Load() {
			return paint("[BLOCKED — /allow or /deny]", cBrightAmber, cBold)
		}
		parts := []string{humanDur(time.Since(started))}
		if n := watchers.Load(); n > 0 {
			parts = append(parts, fmt.Sprintf("%d◉", n))
		}
		chip := paint("["+strings.Join(parts, " · ")+"]", cDim)
		if workingUntil.Load() > time.Now().UnixNano() {
			return chip + " " + paint(spinnerFrame()+" "+label+" is working…", cBrightGreen, cBold)
		}
		return chip
	}
	noteActivity := func(e api.Event) {
		workingUntil.Store(workingDeadline(e, time.Now(), workingUntil.Load()))
	}
	for _, e := range s.Events { // seed from history: it may be working right now
		noteActivity(e)
	}

	// In watch mode nothing is typed, so the editor stays in its passive form and
	// serves only as the printer.
	// Both halves own a bottom line so the indicator is live either way; only
	// the interactive one takes the keyboard.
	ed := newStatusLine(status)
	if !readOnly {
		ed = newEditor(status)
	}
	defer ed.Close()

	// Repaint while the indicator is animating. Idle costs one comparison a
	// tick and prints nothing.
	go func() {
		tick := time.NewTicker(140 * time.Millisecond)
		defer tick.Stop()
		wasWorking := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				now := workingUntil.Load() > time.Now().UnixNano()
				if now || wasWorking { // animate, then one repaint to clear it
					ed.redraw()
				}
				wasWorking = now
			}
		}
	}()

	ended := make(chan struct{})
	go func() {
		defer close(ended)
		backoff := time.Second
		for ctx.Err() == nil {
			err := c.ViewerStream(ctx, id, func(f api.StreamFrame) {
				switch {
				case f.RawEvent:
					if f.Event.Seq <= baseSeq {
						return // catch-up replay we already printed
					}
					baseSeq = f.Event.Seq
					switch f.Event.EventType {
					case "input_requested":
						if f.Event.Str("kind") == "permission" {
							blocked.Store(true)
						}
					case "viewer_decision", "tool_call", "pre_tool", "response", "file_write":
						// Answered here, answered elsewhere, or the agent moved on.
						blocked.Store(false)
					}
					noteActivity(f.Event)
					ed.Print(renderBlock(f.Event, s, me, termWidth())...)
					if blocked.Load() && !readOnly {
						ed.Print(paint("   ⏸ the agent is waiting — /allow, /deny or /always answers it from here", cAmber))
					}
				case f.Type == "watchers":
					watchers.Store(int64(f.Count))
					ed.redraw()
				case f.Type == "session_ended":
					workingUntil.Store(0)
					ed.Print("", paint(" ── session ended ──", cAmber), "")
				}
			})
			if ctx.Err() != nil || err == nil || err == api.ErrNoAccess {
				return
			}
			ed.Print(paint(fmt.Sprintf(" · stream dropped (%v) — reconnecting in %s", err, backoff), cDim))
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
		}
	}()

	if readOnly {
		<-ended
		ed.Close()
		fmt.Println()
		return 0
	}

	// The stream ending should end the join too, rather than leave a prompt
	// attached to a dead session.
	leave := make(chan struct{})
	go func() {
		<-ended
		ed.Print(paint("  session closed — leaving", cDim), "")
		time.Sleep(300 * time.Millisecond)
		close(leave)
	}()

	in := bufio.NewReader(os.Stdin)
	for {
		select {
		case <-leave:
			goto done
		default:
		}
		lineCh := make(chan struct {
			line string
			stop editorStop
		})
		go func() {
			line, stop := ed.ReadLine(in)
			lineCh <- struct {
				line string
				stop editorStop
			}{line, stop}
		}()
		select {
		case <-leave:
			goto done
		case r := <-lineCh:
			if r.stop != stopNone || ctx.Err() != nil {
				goto done
			}
			line := r.line
			if strings.HasPrefix(line, "/") {
				if quit := joinCommand(c, id, line, ed.Print); quit {
					goto done
				}
				continue
			}
			// Light the indicator the moment enter is pressed, not when the round
			// trip finishes. Sending is itself a network call, so waiting for the
			// agent's first event costs two round trips before the terminal admits
			// anything is happening. The window is deliberately short: if no real
			// activity follows, this optimistic guess expires on its own rather
			// than claiming a queued message woke the agent.
			optimistic := time.Now().Add(sendLinger).UnixNano()
			if optimistic > workingUntil.Load() {
				workingUntil.Store(optimistic)
				ed.redraw()
			}
			if err := c.PostComment(id, line); err != nil {
				workingUntil.Store(0)
				ed.Print(paint("  ✗ not sent: "+err.Error(), cRed))
			}
		}
	}
done:
	ed.Close()
	cancel()
	fmt.Println()
	return 0
}

// pendingPermission reports whether the last thing that happened in a session
// was a permission request nobody answered — i.e. the agent is stuck waiting.
// Anything that could only have happened after a verdict clears it.
func pendingPermission(events []api.Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].EventType {
		case "input_requested":
			if events[i].Str("kind") == "permission" {
				return true
			}
		case "viewer_decision", "tool_call", "pre_tool", "response", "file_write":
			return false
		}
	}
	return false
}

const (
	// heartbeatLinger must exceed the pty capture's flush interval, or the
	// indicator strobes between the batches of a single reply.
	heartbeatLinger = 3 * time.Second
	// sendLinger is how long the indicator holds on the optimistic guess that a
	// message we just sent is about to wake the agent. Short, because a comment
	// delivered to an idle agent may sit queued until its next turn.
	sendLinger = 20 * time.Second
	// turnCeiling bounds an open turn: a hooks agent that dies mid-turn never
	// sends the closing event, and without this the indicator would claim it is
	// still working for the rest of the session.
	turnCeiling = 10 * time.Minute
)

// workingDeadline returns the instant the "is working" indicator should go
// quiet, given one event, the time now, and the deadline currently in force.
// Zero means not working. See cmdJoin for why the two captures need different
// rules and why the later deadline wins.
func workingDeadline(e api.Event, now time.Time, current int64) int64 {
	var until int64
	switch e.EventType {
	case "prompt", "pre_tool", "tool_call", "file_write":
		until = now.Add(turnCeiling).UnixNano()
	case "output", "stream_start":
		until = now.Add(heartbeatLinger).UnixNano()
	case "response", "input_requested":
		return 0 // the turn ended, or it is waiting on a human
	default:
		return current
	}
	return max(until, current)
}

// spinnerFrame is the current cell of a braille spinner, driven by the clock so
// every repaint advances it without anybody owning the frame counter.
func spinnerFrame() string {
	const frames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	r := []rune(frames)
	return string(r[(time.Now().UnixNano()/int64(120*time.Millisecond))%int64(len(r))])
}

// signedInName is the display name of the signed-in account, used to mark the
// session owner's own turns as "you". Empty when not resolvable.
func signedInName() string {
	c := auth.Load()
	if c == nil || c.User == nil {
		return ""
	}
	if c.User.Name != "" {
		return c.User.Name
	}
	return c.User.Email
}

// printSessionHeader frames a session above its transcript.
func printSessionHeader(s api.Session, w int) {
	fmt.Println()
	fmt.Println(paint(strings.Repeat("─", w), cDim))
	printSession(s)
	fmt.Println(paint(strings.Repeat("─", w), cDim))
	fmt.Println()
}

// joinCommand handles a /slash line typed during a join. Returns true to leave.
func joinCommand(c *api.Client, id, line string, say func(...string)) bool {
	fields := strings.Fields(line)
	switch fields[0] {
	case "/q", "/quit", "/exit", "/leave":
		return true
	case "/url", "/link":
		say("  " + sessionURL(id))
	case "/info":
		s, err := c.GetSession(id)
		if err != nil {
			say(paint("  ✗ "+err.Error(), cRed))
			break
		}
		say(paint(fmt.Sprintf("  %s · %s · %s · %s · %d watching · agent %s",
			s.Title, s.Status, s.Agent, plural(s.EventCount, "event"),
			s.WatcherCount, yesNo(s.AgentConnected)), cDim))
	case "/who":
		s, err := c.GetSession(id)
		if err != nil {
			say(paint("  ✗ "+err.Error(), cRed))
			break
		}
		say(paint(fmt.Sprintf("  owner %s · %d watching · %s",
			s.OwnerHandle, s.WatcherCount, plural(s.ShareCount, "grant")), cDim))
	case "/tail":
		s, err := c.GetSession(id)
		if err != nil {
			say(paint("  ✗ "+err.Error(), cRed))
			break
		}
		ev := visibleEvents(s.Events)
		if n := 5; len(ev) > n {
			ev = ev[len(ev)-n:]
		}
		for _, e := range ev {
			say(renderBlock(e, s, signedInName(), termWidth())...)
		}
	case "/allow", "/deny", "/always":
		// Answering a permission is comment-level authorization server-side: the
		// same right that lets you talk to the session lets you unblock it, which
		// is the point — the owner is not always looking at their terminal.
		verdict := strings.TrimPrefix(fields[0], "/")
		if err := c.PostDecision(id, verdict); err != nil {
			say(paint("  ✗ "+err.Error(), cRed))
			break
		}
		note := "  ✓ " + verdict + " sent — the agent resumes within a second"
		if verdict == "always" {
			note = "  ✓ always sent — this tool will not ask again for the rest of the session"
		}
		say(paint(note, cGreen))
	case "/fork":
		say(paint("  live claude --session "+short(id), cBold))
	case "/stop":
		if _, err := c.Stop(id); err != nil {
			say(paint("  ✗ "+err.Error(), cRed))
			break
		}
		say(paint("  session stopped", cAmber))
		return true
	case "/help", "/?":
		say(
			paint("  /allow  let the waiting tool run  /deny   refuse it", cDim),
			paint("  /always allow this tool from now on", cDim),
			paint("  /info   session status           /who    owner, watchers, grants", cDim),
			paint("  /tail   reprint the last turns   /url    web link", cDim),
			paint("  /fork   continue it locally      /stop   end it (owner)", cDim),
			paint("  /quit   leave", cDim),
			paint("  anything else you type is delivered to the agent", cDim),
			paint("  ↑/↓ recall what you sent · Ctrl-U clear · Ctrl-W delete word", cDim),
		)
	default:
		say(paint("  ? unknown command — /help", cDim))
	}
	return false
}

// ── event glyphs ──────────────────────────────────────────────────────────────

// eventMark is the glyph + colour per event type, mirroring the web viewer so
// the terminal and the browser read the same.
func eventMark(t string) (string, string) {
	switch t {
	case "prompt":
		return "›", cAmber
	case "response":
		return "◆", cGreen
	case "tool_call":
		return "▸", cAmber
	case "pre_tool":
		return "⟳", cDim
	case "file_write":
		return "✎", cRed
	case "output":
		return "·", cDim
	case "viewer_comment":
		return "✉", cViolet
	case "input_requested":
		return "⌐", cAmber
	case "viewer_decision":
		return "⏎", cGreen
	case "quota_exceeded":
		return "⚠", cRed
	default:
		return "•", cDim
	}
}

// hidden events are internal stream boundaries with no content — the web viewer
// drops them too, and in a terminal they are pure noise between real turns.
var hiddenEvents = map[string]bool{"stream_start": true, "stream_end": true}

// renderEvent formats one event as a single terminal line, for the compact
// contexts where a full block would drown the surrounding summary:
// "14:02:11 ▸ tool_call  Bash — git status".
func renderEvent(e api.Event, width int) string {
	glyph, color := eventMark(e.EventType)
	ts := paint(e.TS.Local().Format("15:04:05"), cDim)
	label := paint(fit(e.EventType, 15), color)
	if width < 40 {
		width = 40
	}
	return fmt.Sprintf("%s %s %s %s", ts, paint(glyph, color), label,
		clipLine(sanitize(eventBody(e)), width-28))
}

// eventBody is the human-readable text of an event: the message for chat-shaped
// events, the tool + target for work steps.
func eventBody(e api.Event) string {
	if who := e.Str("username"); who != "" && (e.EventType == "prompt" || e.EventType == "viewer_comment") {
		return paint(who, cBold) + ": " + e.Str("message", "text", "content")
	}
	if t := e.Str("message", "text", "content"); t != "" {
		return t
	}
	tool := e.Str("tool_name", "tool", "name")
	target := e.Str("file_path", "path", "file", "command")
	switch {
	case tool != "" && target != "":
		return paint(tool, cBold) + " — " + target
	case tool != "":
		return paint(tool, cBold)
	case target != "":
		return target
	}
	// Last resort: the payload's own keys, so nothing renders as an empty line.
	var keys []string
	for k, v := range e.Payload {
		keys = append(keys, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(keys, " ")
}
