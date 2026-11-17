-- Resolver-policy replies (127.255.255.0/24) are not blocklist listings. Older
-- control builds stored them as listed rows; correct the history so the
-- dashboard and metrics do not report a false reputation hit.
BEGIN;

UPDATE control.blocklist_results
SET listed = false
WHERE listed
  AND response ~ '(^|,)127\.255\.255\.[0-9]+(,|$)';

UPDATE control.outbound_ips oi
SET last_listed_at = NULL
WHERE oi.last_listed_at IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM control.blocklist_results br
    WHERE host(br.ip) = host(oi.address)
      AND br.listed
      AND br.checked_at > now() - interval '24 hours'
  );

INSERT INTO schema_migrations (version) VALUES ('0019_blocklist_sentinel')
ON CONFLICT (version) DO NOTHING;

COMMIT;
