-- lint: new-table
-- Taboola's campaign groups, from each account's group list. A group that
-- has left the list for 15 minutes is gone (deleted in Realize). Its
-- campaigns stay in the campaign list with their old status, so Intel
-- records GROUP_DELETED for them instead (Realize: "Campaign Group Was
-- Deleted").

CREATE TABLE intel.tb_group (
    group_id BIGINT PRIMARY KEY,
    account TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    gone_at TIMESTAMPTZ
);
CREATE INDEX ON intel.tb_group (account);

-- When each account's group list was last read. A campaign whose group is
-- not in the list counts as in a deleted group only once a list read 15
-- minutes after the campaign was first seen still lacks the group.
CREATE TABLE intel.tb_group_list (
    account TEXT PRIMARY KEY,
    fetched_at TIMESTAMPTZ NOT NULL
);
