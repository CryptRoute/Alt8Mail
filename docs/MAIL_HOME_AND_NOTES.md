# Mail Home, Pulse and sticky notes

Alt8Mail 0.13 makes the empty reading pane a working home. It stays the
default **Mail Home** tab, with a second tab called **Pulse** for private
mailbox analytics and a temporary **Search report** tab when a search starts
from Pulse.

## Mail Home and Pulse

- **Mail Home** keeps the familiar no-conversation-selected panel: unread
  count, next meeting, security posture, keyboard hints and now the sticky
  note.
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

## Sticky note

Each mailbox can have **one** short note, persisted in PostgreSQL through the
existing user-preferences document:

- Markdown-lite: `**bold**`, `*italic*`, `[label](https://…)`, `- [ ] Task` and
  `- [x] Done`.
- Four tint colors: yellow (default), blue, pink and green.
- Collapses to one line; the collapsed state is remembered.
- Appears above the Inbox list, on Mail Home, and in the Calendar agenda
  ("Today") view. On Mail Home it can be dragged; the position is kept in
  session state and in the mailbox preferences.
- **New sticky note** is available under **New event** in the New mail split
  menu, or use the pin prompt on Mail Home.
- **Pin a conversation**: drag a conversation row onto the note. The note gets a
  click-through linked line. Snoozing a conversation offers to add it to the
  note after the snooze succeeds.
- **Dismiss**: hide for the current session, or turn the note off permanently.
  Re-enable it in **Settings → Mail → Sticky note**.
- **Shared mailboxes**: when the current folder is a delegated mailbox, the
  owner's sticky note is loaded and saved as a team note. Everyone with access
  sees the same note, and edits are audited with the editor's identity.

Tenant administrators can turn sticky notes on or off in **Policies →
Features**. The default is on, including for existing tenants.

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
