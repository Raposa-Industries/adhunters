-- Asks an investigation to stop. One that has not started stops at once; a
-- running one stops at its next visit and keeps what it found. Returns false
-- when it had already ended.
CREATE FUNCTION raposa_api.stop_investigation_v1(p_id BIGINT)
RETURNS BOOLEAN
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    UPDATE raposa.investigation
    SET status = 'stopped', stop_requested = TRUE, stage = 'done',
        stage_note = 'stopped before it started', completed_at = now()
    WHERE id = p_id AND status = 'waiting' AND claim_token IS NULL;
    IF FOUND THEN
        PERFORM raposa.emit(p_id, 'finished', 'Investigation stopped', 'stopped before it started');
        RETURN TRUE;
    END IF;
    UPDATE raposa.investigation
    SET stop_requested = TRUE, next_visit_at = LEAST(next_visit_at, now())
    WHERE id = p_id AND status IN ('waiting', 'running');
    RETURN FOUND;
END;
$$;
