-- Прежние записи аудита могут ссылаться на удалённые устройства, поэтому при возврате
-- внешнего ключа они сохраняются, а старые строки не проверяются.
ALTER TABLE audit_events ADD CONSTRAINT audit_events_device_id_fkey
    FOREIGN KEY (device_id) REFERENCES devices(id) NOT VALID;
