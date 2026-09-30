-- Each campaign with every campaign it continues (copied from, back to the
-- first; depth 0 is itself). root_id names the whole line.
CREATE VIEW intel_api.campaign_line_v1 AS
SELECT campaign_id, ancestor_id, depth, root_id FROM intel.campaign_line;
