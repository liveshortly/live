#!/usr/bin/env python3
"""Record a PTY agent's raw output as a replay fixture for `live`'s PTY path.

    scripts/record-pty.py OUT.jsonl [--input 'text\\r' --wait 5 ...] -- CMD ARGS...

Runs CMD in a 40x120 pseudo-terminal (the same size `live` gives headless runs),
optionally types scripted input (each --input is sent once --prompt has been
printed since the previous input, or output has been idle for --idle seconds
when no --prompt is given, or after --wait seconds at the latest), and writes
one JSON line per output chunk: {"t_ms": <ms since start>, "data": "<text>"}.
Replay tests feed these chunks through the same idle batching + ANSI scrubbing
as runPTYMode (cmd/live/harness_replay_test.go).

No network, no LiveShortly server: this captures what the agent prints, nothing
more. Only use it with agents that cost nothing to run (e.g. a local ollama
model) unless the owner says otherwise.
"""
import argparse, codecs, fcntl, json, os, pty, select, struct, sys, termios, time

ap = argparse.ArgumentParser()
ap.add_argument("out")
ap.add_argument("--input", action="append", default=[], help="text to type (use \\r for Enter)")
ap.add_argument("--idle", type=float, default=3.0, help="idle seconds before typing the next input")
ap.add_argument("--wait", type=float, default=120.0, help="max seconds to wait before each input")
ap.add_argument("--prompt", default="", help="only type the next input once this text has been printed since the last one")
ap.add_argument("--timeout", type=float, default=300.0, help="hard stop")
argv = sys.argv[1:]
if "--" not in argv:
    ap.error("missing -- CMD")
i = argv.index("--")
a, cmd = ap.parse_args(argv[:i]), argv[i + 1:]

pid, fd = pty.fork()
if pid == 0:
    os.execvp(cmd[0], cmd)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))

start = time.monotonic()
dec = codecs.getincrementaldecoder("utf-8")("replace")
inputs = [s.encode().decode("unicode_escape") for s in a.input]
last_out = phase_start = start
seen = ""  # output since the last input, for --prompt
with open(a.out, "w") as f:
    while time.monotonic() - start < a.timeout:
        now = time.monotonic()
        ready = a.prompt in seen and now - last_out >= (0.5 if a.prompt else a.idle)
        if inputs and (ready or now - phase_start >= a.wait):
            os.write(fd, inputs.pop(0).encode())
            phase_start = last_out = now
            seen = ""
        elif not inputs and a.prompt and a.prompt in seen and now - last_out >= a.idle:
            break  # back at the prompt with nothing left to type
        r, _, _ = select.select([fd], [], [], 0.05)
        if not r:
            continue
        try:
            b = os.read(fd, 8192)
        except OSError:
            break  # EIO: child exited
        if not b:
            break
        last_out = time.monotonic()
        text = dec.decode(b)
        seen = (seen + text)[-4096:]
        if text:
            f.write(json.dumps({"t_ms": int((last_out - start) * 1000), "data": text}) + "\n")
try:
    os.kill(pid, 9)
except ProcessLookupError:
    pass
os.waitpid(pid, 0)
