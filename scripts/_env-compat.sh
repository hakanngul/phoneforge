# shellcheck shell=bash
# Sourced by the build/sign scripts. Environment variables moved from PF_* to PL_*
# when PhoneForge became Pocketlab; an old PF_<name> still works when PL_<name> is unset.
for _n in VERSION CODESIGN_IDENTITY KEYCHAIN TEAM_ID BUNDLE_PREFIX WDA_HOME WDA_TARBALL CACHE_DIR; do
  _new=PL_$_n; _old=PF_$_n
  if [ -z "${!_new:-}" ] && [ -n "${!_old:-}" ]; then export "$_new=${!_old}"; fi
done
unset _n _new _old
