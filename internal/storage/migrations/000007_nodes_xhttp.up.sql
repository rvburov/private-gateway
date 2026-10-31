ALTER TABLE nodes ADD COLUMN xhttp_config jsonb;
ALTER TABLE nodes ADD CONSTRAINT nodes_xhttp_config_object
    CHECK (xhttp_config IS NULL OR jsonb_typeof(xhttp_config) = 'object');
