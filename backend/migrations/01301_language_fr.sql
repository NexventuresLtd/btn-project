-- +goose Up
-- SRS §9.1 / risk 9: French joins en / am / rw (F5).
ALTER TABLE "User" DROP CONSTRAINT IF EXISTS "User_language_check";
ALTER TABLE "User" ADD CONSTRAINT "User_language_check" CHECK ("language" IN ('en','am','rw','fr'));
-- +goose Down
UPDATE "User" SET "language"='en' WHERE "language"='fr';
ALTER TABLE "User" DROP CONSTRAINT IF EXISTS "User_language_check";
ALTER TABLE "User" ADD CONSTRAINT "User_language_check" CHECK ("language" IN ('en','am','rw'));
