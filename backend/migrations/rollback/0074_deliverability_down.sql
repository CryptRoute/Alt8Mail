BEGIN;
DROP TABLE IF EXISTS public.campaign_links;
DROP TABLE IF EXISTS public.campaign_events;
DROP TABLE IF EXISTS public.campaign_tracking;
DROP TABLE IF EXISTS public.dmarc_records;
DROP TABLE IF EXISTS public.dmarc_reports;
DELETE FROM schema_migrations WHERE version = '0074_deliverability';
COMMIT;
