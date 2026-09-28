-- One investigation and where it stands.
CREATE VIEW raposa_api.investigation_v1 AS
SELECT id, creative_id, ad_id, mode, origin, requested_by, requested_at, status, stop_requested,
       stage, stage_note, target_device, burn_scope, rung_reached, breach_rung, white_page_id,
       visits_target, visits_done, variants_count, bytes_used, is_cloaked, cloaked_confidence,
       retry_of, attempt, next_visit_at, started_at, completed_at
FROM raposa.investigation;
