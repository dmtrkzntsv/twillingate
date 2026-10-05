-- 030: server_stats, the daily history the usage tool reads (each project's
-- size and counts, the database's size, the caps in force), is called
-- usage_history, after the surface that shows it. A rename: no row is
-- copied, and no view or index names the table.
ALTER TABLE server_stats RENAME TO usage_history;
