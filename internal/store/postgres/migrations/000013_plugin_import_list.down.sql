DROP INDEX plugin_imports_list;
ALTER TABLE plugin_imports
    DROP COLUMN plugin,
    DROP COLUMN started_at,
    DROP COLUMN retry_of;
