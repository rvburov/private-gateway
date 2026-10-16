-- Retain immutable audit events by UUID after the revoked device is deleted.
ALTER TABLE audit_events DROP CONSTRAINT audit_events_device_id_fkey;
