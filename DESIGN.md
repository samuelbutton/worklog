# Worklog: design

A local, append-only record of the founder's work with an assistant: every draft the assistant proposes, every change the
founder makes to it, what was finally sent or decided, and what came of it. It was built for Peer Review's go-to-market work
(milestones M0–M2: openers, follow-ups, call prep and notes, failure reviews, agreements, decisions). It knows nothing about Peer
Review, and nothing about any particular assistant.

## Why

Three uses, in order of when they pay off:

1. **Running the work.** One place that says:
   - what was drafted;
   - what was sent, and to whom;
   - what is waiting on a reply.
2. **Learning from it.** The Friday review asks which openers got replies and what to change. That needs the sent text joined
   to its outcome, not reconstructed from memory.
3. **Data.** Each proposal with its final form is an edit pair:
   - what the assistant wrote;
   - what the founder changed;
   - the instructions in between;
   - the outcome.

   Pairs like these are what preference training and prompt tuning need, and they cannot be recreated later.

## Rules

- **Nothing is lost.**
  - Events are appended, never edited or deleted.
  - A correction is a new event that points at the old one.
  - Content is stored once, under its sha256, and never overwritten.
- **Facts are recorded, judgements are annotated.**
  - What happened (a version, a send, a reply) is written when it happens.
  - Readings of it ("this edit was about tone", "this reply is positive") are `annotation` events, with who made them.
- **Provider-agnostic.**
  - The only interface is a command-line tool and a folder of plain files.
  - Any assistant that can run a command can record its work: Claude Code, Codex, Cursor, a script, or the founder by hand.
  - The schema says `assistant`, never a vendor. An assistant may describe itself in `WORKLOG_AGENT` and `WORKLOG_MODEL`.
- **Local only.**
  - The data holds prospects' names and email addresses.
  - It lives in `~/worklog` (mode 0700), is never committed to a repository and is never sent anywhere.
  - Back it up the way the Mac is backed up (Time Machine).
- **Flexible.**
  - Every event has a free `data` object.
  - Kinds of work, stages, statuses, outcomes and tags are vocabularies, not fixed lists; a new kind of work needs no code change.
  - `worklog log <type>` records any event at all.
  - Schema changes are additive (`v` on every event).

## Where things are

| Path | What |
|---|---|
| `~/code/samuelbutton/worklog` | This tool (Go, standard library only). `make install` puts it in `~/.local/bin` |
| `~/worklog/events/YYYY-MM-DD.jsonl` | **The record.** One JSON event per line, by UTC day. The only source of truth |
| `~/worklog/blobs/sha256/ab/<hash>` | Every version's text, pasted replies and long messages, stored once by hash, read-only |
| `~/worklog/work/<item>/` | Working copies: the drafts being edited now. Edit them in any editor; saves are recorded |
| `~/worklog/archive/<name>/` | Compressed copies of folders worth keeping, such as an assistant's own transcripts |
| `~/worklog/index/` | Caches rebuilt from the events (`worklog reindex`); safe to delete |
| `~/worklog/errors.log` | Failures from `watch` and `archive`, which keep going instead of stopping |

`WORKLOG_HOME` moves the data elsewhere; the tests use it.

## The model

**An item** is one piece of work with a lifecycle: an email to a person, a call (prep, then notes), a document, a decision.
It has:
- an id, a readable slug such as `email-opener-acme`;
- a kind and a title;
- optionally, a milestone task (`M1.1`), an organization, people and free tags.

**A version** is one state of a file in the item's folder, with:

- its author: `assistant` or `user`;
- how it arrived:
  - `cli` or `paste`: given to the tool;
  - `scan`: a save found in the work folder;
  - `workcopy`: the working copy as it stood when marked final;
- its stage: `proposed`, `revised`, `edited` or `final`;
- its parent version.

The chain proposal → instruction → revision → founder's edit → final is the core data.

**An event** is one line:

```json
{"v":1,"id":"e_mh7k2c9q_3fa91c","ts":"2026-10-27T14:03:11.123Z","type":"version","actor":"assistant","item":"email-opener-acme",
 "session":"…","data":{"blob":"9f2c…","path":"draft.md","stage":"proposed","via":"paste","bytes":812,"agent":"claude-code","model":"claude-opus-5-5"}}
```

| Type | Data |
|---|---|
| `item` | kind, title, task, org, people, tags, file |
| `version` | blob, path, stage, via, bytes, parent, note, deleted |
| `instruction` | text: what the founder asked the assistant to change ("shorter, drop the second offer") |
| `status` | status (`drafting`, `ready`, `sent`, `waiting`, `done`, `dropped`, …), channel, to, note |
| `outcome` | result (`replied`, `positive`, `negative`, `meeting`, `no-reply`, `bounced`, …), note, blob of the reply |
| `decision` | question, options, choice, why, evidence |
| `note` | text: an observation about the work or the process |
| `message` | role, text (or blob and preview when long): a message from any conversation with an assistant |
| `annotation` | target (event or item), labels, note: a judgement, with who made it |
| `archive` | name, folder, files copied |
| anything else | whatever `worklog log` was given |

## Who wrote what

Attribution has to be right, or the edit pairs are worthless. The rule that makes it simple:

- **An assistant never writes a working copy directly.** It hands its text to `worklog propose`, which records the version and
  then writes the file.
- **So every other change in the work folder is the founder's.**
  - `worklog watch` notices those saves as they happen.
  - Every command also looks first, so a save is always recorded before anything that follows it. A proposal that replaces the
    file never loses the founder's unrecorded edit.

This needs no hooks into any assistant, and works the same whether the founder edits in VS Code, TextEdit or vim.

## A day of work

```bash
worklog new email "Opener: Acme" --task M1.1 --org Acme --person "Jane Doe <jane@acme.com>"   # prints the item and its file
worklog propose email-opener-acme --stdin < draft.txt          # the assistant's draft (it pipes its text in)
#   … the founder edits ~/worklog/work/email-opener-acme/draft.md, or asks for changes:
worklog instruct email-opener-acme "shorter; lead with their own agent"
worklog propose email-opener-acme --stdin < revised.txt
#   … the founder pastes into Gmail, adjusts, sends, and pastes back what was sent:
worklog final email-opener-acme --stdin --via paste             # or with no input: the working copy as it is
worklog status email-opener-acme sent --channel gmail --to jane@acme.com
worklog outcome email-opener-acme positive --stdin              # paste the reply
worklog decide "Which segment first?" --options "sales,front office" --choice sales --why "…"
worklog note "Openers with a named failure get read; generic ones don't"
```

## Reading it back

| Command | Shows |
|---|---|
| `worklog today` (or `--date`) | The day as a timeline |
| `worklog items` | Open work and its state |
| `worklog show <item>` | An item's history and how much the founder changed it |
| `worklog diff <item>` | What the founder changed in the assistant's last proposal |
| `worklog export pairs` | One JSON line per item: first and last proposal, final, instructions, edit size, outcomes and annotations. This feeds the Friday review and, later, training |
| `worklog export events` | Everything, for any other analysis |
| `worklog pending` | Instructions not yet answered by a new proposal, with the founder's feedback on the proposal before them |
| `worklog serve` | A local page (loopback only) for the founder: read, edit, compare, give feedback, confirm, mark sent, record outcomes. It calls the same functions as the commands, so the record is the same |

## Emails specifically

The send happens in the founder's mail client, which the tool cannot see. The final text arrives one of two ways:

1. **Pasted back.** The founder pastes what was sent (`--via paste`). This is the most faithful today, because last edits
   happen in the mail client.
2. **The working copy.** The founder edits the working copy into its final form, sends it, and says so (`worklog final <item>`).

Matching sent mail automatically is a later adapter (below).

## Conversations

The record does not depend on any assistant's transcripts. Two optional ways to keep conversations too:

- `worklog message` records a message from any conversation, by role.
- `worklog archive <name> <folder>` keeps a compressed copy of any tool's own transcript folder. Some tools delete old
  transcripts on their own; Claude Code does after 30 days unless `cleanupPeriodDays` is raised.

## Later

- **Adapters, each optional and outside the core:**
  - matching sent mail and replies from a mailbox export or connection;
  - importing an assistant's transcript format into `message` events.
- A daily digest at the end of each day: drafted, sent, replied, decided.
- A SQLite index for queries across months; the JSONL stays the record.
- **Annotation passes over the edit pairs:**
  - what kind of change: length, tone, facts, offer, personalization;
  - first by an assistant, then checked by the founder.
