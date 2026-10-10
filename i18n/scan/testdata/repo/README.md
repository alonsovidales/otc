A fixture repository for i18n/scan's tests: one small file per surface, with
text that counts and text that doesn't. The expected findings are in
../repo.golden (go test ./i18n/scan -run TestFixtureRepo -golden rewrites it).
