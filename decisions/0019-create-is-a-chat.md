# 0019. Create is a chat of sessions, turns and picked items

Date: 2026-10-01. Status: accepted. Replaces the pages of 0015.

## Context

The owner, after using the brief pages (2026-10-01 02:36): Create should be
more like auto-creative, with nothing about briefs. A person chooses the
vertical, so it is clear which folder what it made is saved in, and names
or chooses a session, which is a folder inside the vertical's. What
auto-creative lacked is iteration: choose an image or a headline, generate
a variation, choose again, and go on or stop. The page is a chat box where
a person selects images or headlines, or nothing, and sends a prompt.

## Decision

- A **session** is a vertical (our fixed list, moved to `shared/verticals`
  so Spy and Create read the same one) and a name, unique in its vertical.
  The library already keeps `<vertical>/<set>` folders, so a session's
  saves go in one set named after it, made by its first save.
- A **turn** is the prompt, the picked items in order, and how many
  pictures (0-8) and headlines (0-20). Picked pictures go to the picture
  model's edit endpoint and are changed as the prompt says (a close
  variation with no prompt); picked headlines are the ones new headlines
  vary; picked pictures are what new headlines are written for. With no
  pictures picked, several pictures get one brief each from the text call
  so they differ. Every made item keeps the ids it came from.
- An **item** is a picture or headline: made, uploaded, from the library,
  or typed. All of them can be picked.
- Nothing reaches the library until a person saves; made pictures carry
  the person's AI label, others are left unset.
- `create_api` publishes `session_v1`, `turn_v1`, `item_v1`,
  `session_save_v1` and `new_session_v1`, `send_turn_v1`, `save_items_v1`,
  which the page calls too. Desk's catalog moves to them.
- The worker, the restart rule (a picture left running fails, the rest runs
  again), OpenAI only, English headlines and warnings that never block are
  kept from 0015.

## Consequences

- The brief tables and their rows stay where they are, unread; the brief
  views and functions stay published until a later contract step drops
  them (that step needs its own decision). Old brief links land on the chat.
- Performing-ad analysis and angles are no longer steps of their own: a
  person picks a library or uploaded picture and asks for what they want.
