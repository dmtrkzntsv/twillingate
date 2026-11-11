-- 048: a monitor may expect a redirect. redirect_to is one of the project's
-- other exact origins, where the check must end up (https://www.example.com
-- answering from https://example.com), or '' for none. Every monitor already
-- there expects none, as before. A column with a default.
ALTER TABLE monitors ADD COLUMN redirect_to TEXT NOT NULL DEFAULT '';
