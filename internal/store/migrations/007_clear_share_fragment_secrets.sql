-- Share fragments are one-time capabilities. Preserve only the verification
-- hash and remove plaintext fragments stored by older releases.
UPDATE shares
SET fragment_secret = NULL
WHERE fragment_secret IS NOT NULL;

CREATE TRIGGER shares_fragment_secret_no_insert
BEFORE INSERT ON shares
WHEN NEW.fragment_secret IS NOT NULL
BEGIN
	SELECT RAISE(ABORT, 'share fragment secrets must not be persisted');
END;

CREATE TRIGGER shares_fragment_secret_no_update
BEFORE UPDATE OF fragment_secret ON shares
WHEN NEW.fragment_secret IS NOT NULL
BEGIN
	SELECT RAISE(ABORT, 'share fragment secrets must not be persisted');
END;
