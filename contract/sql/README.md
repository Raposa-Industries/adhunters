# Published views

One folder per service (`tracks/`, `raposa/`, …), one file per view version,
for example `tracks/ad_hourly_v1.sql`. Each file is the exact `CREATE VIEW`
the owning service's migration runs. Another service may read what is listed
here and nothing else.
