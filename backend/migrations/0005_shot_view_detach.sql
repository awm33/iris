-- +goose Up
-- Removing a cataloged view detaches shots that framed it (the shot's other
-- intent survives). The original NO ACTION FK made in-use views undeletable,
-- contradicting the API's semantics.
ALTER TABLE shots DROP CONSTRAINT shots_view_id_fkey;
ALTER TABLE shots ADD CONSTRAINT shots_view_id_fkey
    FOREIGN KEY (view_id) REFERENCES views(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE shots DROP CONSTRAINT shots_view_id_fkey;
ALTER TABLE shots ADD CONSTRAINT shots_view_id_fkey
    FOREIGN KEY (view_id) REFERENCES views(id);
