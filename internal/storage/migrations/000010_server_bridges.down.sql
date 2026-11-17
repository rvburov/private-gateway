-- Удалённые секреты нельзя восстановить обратной миграцией. Полный откат выпуска
-- выполняется штатным восстановлением согласованной резервной копии базы.
DO $$ BEGIN
    RAISE EXCEPTION 'server bridge migration requires restoring the release database backup';
END $$;
