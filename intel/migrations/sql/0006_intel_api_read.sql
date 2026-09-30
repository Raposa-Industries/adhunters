-- Readers get intel_api's views through the intel_api_read role, which the
-- box setup creates and grants to each reading service's login.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'intel_api_read') THEN
        GRANT USAGE ON SCHEMA intel_api TO intel_api_read;
        GRANT SELECT ON ALL TABLES IN SCHEMA intel_api TO intel_api_read;
    END IF;
END;
$$;
