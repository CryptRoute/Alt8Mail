\set ON_ERROR_STOP on
DELETE FROM public.users
WHERE email LIKE 'soak-preflight-%'
   OR (email LIKE 'soak-20261029-%'
       AND email NOT IN (
         'soak-20261029-0001@a8m.test',
         'soak-20261029-0002@a8m.test',
         'soak-20261029-0003@a8m.test'
       ));
