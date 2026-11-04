# Mail Home, Pulse and the Notes desk

Alt8Mail makes the empty reading pane a working home. It stays the
default **Mail Home** tab, with a second tab called **Pulse** for private
mailbox analytics and a temporary **Search report** tab when a search starts
from Pulse.

## Mail Home and Pulse

- **Mail Home** keeps the familiar no-conversation-selected panel: unread
  count, next meeting, security posture, keyboard hints and the Notes board.
- **Pulse** shows a year selector, messages, sent/received, reading time and
  reaction totals. The Mail flow chart stacks received and sent mail by month
  or by ISO week. Click a bar to inspect that period and open its messages as a
  search report.
- The Pulse search field opens a **Search report** tab with match count,
  conversation count, unique senders, a monthly match chart, top senders and a
  click-through result list.
- The active home tab and the current search report are kept in
  `sessionStorage`. They survive a browser reload and are cleared on sign-out,
  or when the browser tab is closed. Nothing is written to another user's
  session.

## Notes desk

Each mailbox has a **Notes desk** that can hold up to 100 notes. Notes are
stored in PostgreSQL in `user_sticky_notes`, scoped by tenant and mailbox
owner, with an audit entry for every create, edit, pin and archive.

- **One Inbox pin.** Exactly one active note can sit above the Inbox list.
  The first note a mailbox creates is pinned automatically. Pinning another
  note moves the pin; the previous note stays in the desk. An **Undo** action
  restores the previous pin.
- **Notes desk.** The persistent pin button in the mail toolbar opens the desk
  with active and archived notes, a search field, note previews, colors,
  pin/unpin, edit and archive actions. The desk is available even when no note
  is pinned and when the Inbox list is empty.
- **Global note search.** Typing in the desk searches every accessible
  mailbox, not only the notes currently rendered. Results match the title,
  body and attached-item metadata. Switch between **All mailboxes** and
  **This mailbox** when delegated mailboxes are available, and search the
  **Archive** tab independently of active notes.
- **Mail search discovery.** A normal mailbox search also returns a compact
  **Notes** strip above the message results. Opening a result goes straight to
  that note, even when it lives in a shared mailbox or the archive.
- **+N chip.** When more active notes exist than the pinned one, the Inbox
  stack shows a `+N notes in desk` chip that opens the full desk.
- **Home board.** Mail Home shows the active notes as draggable cards. The
  board is a view of the same desk, not a second copy.
- **Archive instead of delete.** Archiving removes the Inbox pin and moves the
  note to the desk's Archive tab. Restore brings it back without silently
  reclaiming the Inbox pin.
- **New note** is available from the **New sticky note** action, the persistent
  Notes launcher, the Notes desk and the pin prompt on Mail Home.
- Markdown-lite: `**bold**`, `*italic*`, `[label](https://…)`, `- [ ] Task` and
  `- [x] Done`.
- Four tint colors: yellow (default), blue, pink and green.
- Notes collapse to one line; the collapsed state is remembered per note.
- **Attach conversations, events and files:** drag a conversation row onto a
  note, or open the note editor and use **Conversation**, **Event** and
  **File** pickers. Each note carries up to 12 typed attachments. Selecting an
  attachment opens the conversation, event dialog or Cabinet file; removing it
  leaves the other attachment types intact. Snoozing a conversation offers to
  attach it after the snooze succeeds.
- **Attachment permissions:** conversations are resolved against the mailbox,
  events against calendars visible to that mailbox (owned, shared or
  attended), and files against that mailbox's Cabinet. The server records the
  display metadata so a note stays readable if the source is later removed.
- **Shared-mailbox team desks:** when the current folder is a delegated
  mailbox, the owner's desk is loaded and saved as a team desk. A **Team**
  badge appears on the desk and every note card, the subtitle names the
  mailbox owner and teammate count, and global search labels the owning
  mailbox on each result. Everyone with read access sees the same notes;
  edits require send access. Read-only delegates see a **Read-only** marker and
  cannot create, edit, pin or archive notes. Every edit is audited with the
  editor's identity.

### Migrating the old single note

Migration `0040_notes_desk` creates the multi-note table. The first time a
mailbox opens its desk, the legacy `preferences.stickyNote` document is
converted into one row:

- content, links, color, collapsed state and Home position are preserved;
- a legacy note that was enabled becomes the Inbox pin;
- a legacy note that had been turned off is migrated as an unpinned note;
- the old `/api/sticky-note` endpoint remains for compatibility, but new
  clients use `/api/notes`.

Migration `0041_notes_context` adds typed-link backfill and indexes for
mailbox, archive and attachment lookups. Existing thread links gain
`type: "thread"` and a canonical `id` without changing their display data.

Tenant administrators can turn notes on or off in **Policies → Features**.
The default is on, including for existing tenants.

## Reactions

- The reader toolbar and command bar expose **React** immediately after
  **Reply with AI**, with a visible emoji.
- React is disabled for newsletter/List-Unsubscribe conversations, Spam,
  Drafts, Sent and the virtual Subscriptions folder. The API enforces the same
  rules.
- The picker includes the full categorized emoji catalog, search, recently
  used emoji, quick reactions and AI/semantic suggestions.
- Choose **Private** or **Send to sender**. Sending opens a confirmation with a
  large emoji preview before anything leaves the mailbox. Private reactions
  remain a single click.
- A sent reaction email shows a large emoji and, underneath it,
  `{reactor} from {organization} reacted via Alt8Mail`.
- Tenant administrators can turn reactions off in **Policies → Features**.
