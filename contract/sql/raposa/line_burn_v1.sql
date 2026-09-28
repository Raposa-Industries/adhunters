-- A line one site shows the white page to. ended_at is NULL while it lasts.
CREATE VIEW raposa_api.line_burn_v1 AS
SELECT scope, line_key, rung, visits, dark, other_visits, other_dark, detected_at, checked_at, ended_at
FROM raposa.line_burn;
