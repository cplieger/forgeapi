#!/usr/bin/env bash
# .github/scripts/workflow-command.sh writes exactly one escaped command line.
# WORKFLOW_COMMAND_SH points the suite at another copy for a red-check.
# SC2015: lib.sh verdict helpers return 0.
# shellcheck disable=SC2015
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null
SCRIPT="${WORKFLOW_COMMAND_SH:-$REPO_ROOT/.github/scripts/workflow-command.sh}"

# run <args>...: run the shipped script directly, so a lost exec bit fails too.
run() {
  _rc=0
  "$SCRIPT" "$@" >"$WORK/out" 2>"$WORK/err" || _rc=$?
}

# expect <name> <line> <args>...: the script exits 0 and writes <line> and one LF,
# byte for byte, and nothing on stderr.
expect() {
  local name=$1 line=$2
  shift 2
  run "$@"
  printf '%s\n' "$line" >"$WORK/want"
  [ "$_rc" -eq 0 ] && cmp -s "$WORK/want" "$WORK/out" && [ ! -s "$WORK/err" ] \
    && ok "$name" \
    || no "$name" "rc=$_rc out=$(od -c "$WORK/out") err=$(cat "$WORK/err")"
}

# refused <name> <args>...: the script exits 2 with its usage line and writes nothing.
refused() {
  local name=$1
  shift
  run "$@"
  [ "$_rc" -eq 2 ] && [ ! -s "$WORK/out" ] && grep -q '^usage: ' "$WORK/err" \
    && ok "$name" \
    || no "$name" "rc=$_rc out=$(cat "$WORK/out") err=$(cat "$WORK/err")"
}

if [ ! -f "$SCRIPT" ]; then
  no "precondition: the script exists" "$SCRIPT"
  report
  exit 1
fi
ok "precondition: the script exists"

hostile=$'50% a:b,c\r\nX\n::error::y'

expect "the message escapes %, CR and LF and keeps : and ," \
  '::error::50%25 a:b,c%0D%0AX%0A::error::y' error "" "$hostile"
expect "the title also escapes : and ," \
  '::error title=50%25 a%3Ab%2Cc%0D%0AX%0A%3A%3Aerror%3A%3Ay::50%25 a:b,c%0D%0AX%0A::error::y' \
  error "$hostile" "$hostile"
expect "% is escaped before the escapes it introduces" \
  '::notice title=%25%0D::%25%0A' notice $'%\r' $'%\n'
expect "a literal %0A or %0D in the input is escaped, not passed through" \
  '::notice title=%250A::%250D' notice '%0A' '%0D'
expect "a trailing LF in a value is escaped, not dropped" \
  '::warning title=t%0A::m%0A' warning $'t\n' $'m\n'
expect "an empty title writes no properties" \
  '::notice::plain text' notice "" "plain text"
expect "a mask value is escaped as data" \
  '::add-mask::s%25e%0Ac' add-mask "" $'s%e\nc'
expect "an empty message writes an empty command" \
  '::error title=live lane::' error "live lane" ""

refused "two arguments are refused" error "message"
refused "four arguments are refused" error "t" "m" "extra"
refused "a command with a separator is refused" 'error::x' "" "m"
refused "a command with a newline is refused" $'error\n::warning' "" "m"
refused "an empty command is refused" "" "" "m"

report
