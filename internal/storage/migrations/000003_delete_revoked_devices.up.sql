-- Сохранять неизменяемые события аудита по UUID после удаления отозванного устройства.
ALTER TABLE audit_events DROP CONSTRAINT audit_events_device_id_fkey;
