\set ON_ERROR_STOP on
DELETE FROM public.users
WHERE email LIKE 'soak-preflight-%'
   OR (email LIKE 'soak-20261029-%'
       AND email NOT IN (
         'soak-20261029-0001@meridian.test',
         'soak-20261029-0002@meridian.test',
         'soak-20261029-0003@meridian.test'
       ));
