BEGIN;
DROP TABLE IF EXISTS public.bookings;
DROP TABLE IF EXISTS public.booking_page_members;
DROP TABLE IF EXISTS public.booking_pages;
DELETE FROM schema_migrations WHERE version = '0072_booking_pages';
COMMIT;
