#!/bin/sh
# Écrit la clé API SNCF dans le fichier inclus par nginx.
# Usage : scripts/set-sncf-key.sh <CLE> [fichier de sortie]
set -eu
KEY="${1:?Usage: $0 <CLE_API_SNCF> [fichier]}"
OUT="${2:-$(dirname "$0")/../deploy/nginx/secrets/sncf_auth.inc}"
AUTH=$(printf '%s:' "$KEY" | base64 | tr -d '\n')
umask 077
printf 'proxy_set_header Authorization "Basic %s";\n' "$AUTH" > "$OUT"
echo "Clé écrite dans $OUT — rechargez nginx (nginx -s reload)."
