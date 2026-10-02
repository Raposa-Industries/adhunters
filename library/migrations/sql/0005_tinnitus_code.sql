-- The team's names are a vertical's code and the network letter: BPT, DBT,
-- MMT, NPT, TINT, WLT on Taboola (Vini, 2 Oct 2026). Tinnitus is TIN, not TN.
-- A data change only, and only while it is still TN and no other vertical
-- took TIN: a code someone changed by hand stays, and names already minted
-- (TNT…) keep theirs.
UPDATE library.vertical SET code = 'TIN'
WHERE id = 'tinnitus' AND code = 'TN'
  AND NOT EXISTS (SELECT 1 FROM library.vertical WHERE code = 'TIN');
