-- Historical audit rows may refer to deleted devices, so preserve them when
-- restoring the foreign key without validating old rows.
ALTER TABLE audit_events ADD CONSTRAINT audit_events_device_id_fkey
    FOREIGN KEY (device_id) REFERENCES devices(id) NOT VALID;
