# worklog

A local, append-only record of work done with an assistant: drafts proposed, the founder's edits, what was sent or decided,
and what came of it. It works with any assistant, or none, and keeps everything on this machine in `~/worklog`.

Read [DESIGN.md](DESIGN.md) for the model and the rules.

```bash
make install            # builds ~/.local/bin/worklog
make test
worklog help
```

While working, keep a watcher running so each save to a draft is recorded as it happens (every command also checks first). `make watch-at-login` runs it in the background from login on (`make no-watch-at-login` undoes it); or run it in a terminal:

```bash
worklog watch
```

## For an assistant working with the founder

- Create an item for each piece of work meant to leave the machine or to be decided: `worklog new <kind> <title> …`.
- Propose text only through `worklog propose <item> --stdin`; never write the working copy directly. Any other change there
  counts as the founder's.
- Record what the founder asks you to change with `worklog instruct`, and record finals, statuses, outcomes, decisions and
  notes as they happen.
- Set `WORKLOG_AGENT`, `WORKLOG_MODEL` and `WORKLOG_SESSION` on your commands so your events say who you are.
- Never copy the data folder into a repository or send it anywhere.
