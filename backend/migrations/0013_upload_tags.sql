-- +goose Up
-- Tags travel with the upload so workflow intermediates (gen-fill
-- source/mask flattens) arrive pre-tagged "utility" and stay out of the
-- default Library listing.
ALTER TABLE pending_uploads ADD COLUMN tags text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE pending_uploads DROP COLUMN tags;
