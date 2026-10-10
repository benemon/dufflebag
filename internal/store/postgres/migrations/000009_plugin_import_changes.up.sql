ALTER TABLE plugin_imports ADD COLUMN changes jsonb NOT NULL DEFAULT '[]';

ALTER TABLE plugin_imports DROP CONSTRAINT plugin_imports_source_kind_check;
ALTER TABLE plugin_imports ADD CONSTRAINT plugin_imports_source_kind_check
    CHECK (source_kind IN ('releases-hashicorp', 'github', 'upload'));
