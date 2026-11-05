ALTER TABLE devices ADD COLUMN preferred_exit text NOT NULL DEFAULT 'server';
ALTER TABLE devices ADD CONSTRAINT devices_preferred_exit
    CHECK (preferred_exit IN ('server', 'proxy'));
