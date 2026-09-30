-- One request and where it stands. input is what was asked, as sent.
CREATE VIEW launch_api.request_v1 AS
SELECT id, kind, input, requested_by, origin, state, confirmed_by, result, made_at, decided_at
FROM launch.request;
