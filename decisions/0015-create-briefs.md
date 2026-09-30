# 0015. Create is briefs, a worker and saves into the library

Date: 2026-09-30. Status: accepted.

## Context

The owner asked (2026-09-30) to rebuild Create from scratch on
auto-creative's steps, with its generation moving out of the launcher page
(which becomes Launch) and onto the shared shell. auto-creative's proven
steps: a conversation per set, a plan split into one brief per picture, one
call per picture (a failure stays with its picture), a worker that picks up
again after a restart, and "keep" into a gallery with minted names. Desk
(the chat agent) asked to start briefs and save sets the same way the pages
do. The owner's rules: OpenAI only, for images and headlines; headlines
always in English; Taboola's rules warn and never block; the AI label is
the person's choice.

## Decision

- One binary, `create`, under `/create/` on the Frame. Its rows are in
  `create_app` ("create" is reserved in SQL); `create_api` publishes briefs,
  options and saves, and two functions, `new_brief_v1` and `save_set_v1`,
  that Desk calls and Create's own save button calls too.
- Everything paid runs in a worker from a job table in the database, so a
  page, Desk and a restart all see the same queue. A picture is one job;
  one left running by a stop fails rather than runs again, since it may
  have been paid for. Reads, plans and saves run again.
- Performing ads are read into an analysis the person edits; plans follow
  the edited analysis and the chosen angles. They never go to the image
  model. "Again, with a note" edits one picture; "3 more" asks for a new
  angle.
- Create keeps its references and pictures in its own file store; the
  library (decision 0014) holds what the team keeps. A save writes one set
  there and remembers its id at once, so a retry adds to the same set.
- Spy ads are read through `tracks_api` (the ad's picture address and
  headline) and downloaded on the data box.

## Consequences

- create-web keeps running until Launch replaces it; its generation code
  (`internal/openai`) is Create's and both binaries use it.
- A picture interrupted by a restart needs a person to press "De novo".
- Results (Intel) are not in Create yet.
