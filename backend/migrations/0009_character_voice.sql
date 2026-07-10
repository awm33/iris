-- +goose Up
-- Character voice binding (W4): the vendor voice id the character speaks
-- with (elevenlabs voice for TTS). A CharacterRef with role=voice remains
-- the CLONING-sample slot — this is the resolved vendor identity.
ALTER TABLE characters ADD COLUMN voice_id text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE characters DROP COLUMN voice_id;
