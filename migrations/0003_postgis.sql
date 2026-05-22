-- Phase 7: PostGIS geo column + spatial index for "events near me".

CREATE EXTENSION IF NOT EXISTS postgis;

ALTER TABLE events ADD COLUMN IF NOT EXISTS geom geography(Point, 4326);

-- Backfill any existing rows with coordinates.
UPDATE events
SET geom = ST_SetSRID(ST_MakePoint(lng, lat), 4326)::geography
WHERE lat IS NOT NULL AND lng IS NOT NULL AND geom IS NULL;

-- Keep geom in sync with lat/lng on write.
CREATE OR REPLACE FUNCTION events_set_geom() RETURNS trigger AS $$
BEGIN
    IF NEW.lat IS NOT NULL AND NEW.lng IS NOT NULL THEN
        NEW.geom := ST_SetSRID(ST_MakePoint(NEW.lng, NEW.lat), 4326)::geography;
    ELSE
        NEW.geom := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_events_geom ON events;
CREATE TRIGGER trg_events_geom
    BEFORE INSERT OR UPDATE ON events
    FOR EACH ROW EXECUTE FUNCTION events_set_geom();

CREATE INDEX IF NOT EXISTS idx_events_geom ON events USING GIST (geom);
