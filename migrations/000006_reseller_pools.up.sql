-- Resource pools allocated to reseller users (plan.md §4A). A reseller has
-- exactly one pool; 0 means unlimited for that dimension.

CREATE TABLE reseller_pools (
    user_id      uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    max_accounts integer NOT NULL DEFAULT 0,
    max_disk_mb  integer NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);
