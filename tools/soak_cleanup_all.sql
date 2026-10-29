\set ON_ERROR_STOP on
DELETE FROM public.users
WHERE email LIKE 'soak-preflight-%' OR email LIKE 'soak-20261029-%';
