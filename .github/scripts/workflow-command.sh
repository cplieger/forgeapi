#!/usr/bin/env bash
# workflow-command.sh <command> <title> <message>
#
# Writes one GitHub Actions workflow command line, `::<command> title=<title>::<message>`,
# with no properties when the title is empty. The values are escaped as the Actions
# toolkit's escapeProperty and escapeData do, so no value can end the line and start a
# second command:
# https://github.com/actions/toolkit/blob/main/packages/core/src/command.ts
set -euo pipefail

if [ "$#" -ne 3 ] || [[ ! "$1" =~ ^[a-z][a-z-]*$ ]]; then
  echo "usage: workflow-command.sh <command> <title> <message>" >&2
  exit 2
fi

# % first, or the escapes written after it would be escaped again.
escape_data() {
  REPLY=${1//'%'/%25}
  REPLY=${REPLY//$'\r'/%0D}
  REPLY=${REPLY//$'\n'/%0A}
}

escape_property() {
  escape_data "$1"
  REPLY=${REPLY//:/%3A}
  REPLY=${REPLY//,/%2C}
}

props=""
if [ -n "$2" ]; then
  escape_property "$2"
  props=" title=${REPLY}"
fi
escape_data "$3"
printf '::%s%s::%s\n' "$1" "$props" "$REPLY"
