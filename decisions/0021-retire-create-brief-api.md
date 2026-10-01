# 0021 · create_api drops the brief views and functions

**Decided:** 1 Oct 2026, the contract step [0019](0019-create-is-a-chat.md)
left for later. Asked for by the handoff audit: `create_api.new_brief_v1`
still queued `create_app.job`, which nothing has read since Create became a
chat, so a Desk call to it would wait for good.

Migration 0005 drops `create_api.brief_v1`, `option_v1`, `save_v1`,
`new_brief_v1` and `save_set_v1`, and their contract files go. Nothing calls
them: Desk's catalog moved to the session functions in 0019, and Desk is off.

**Kept:** the brief tables (`create_app.brief`, `option`, `job`, `save` and
the rest) and every row in them, unread. Dropping those is a later decision,
if ever.
