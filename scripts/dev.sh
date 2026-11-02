#!/bin/sh
# Lance l'app en local avec l'image du projet (nginx + open data), même config qu'en production.
# public/ et deploy/nginx/ sont montés : les modifications sont visibles sans reconstruire.
# Usage : scripts/dev.sh [port]   (défaut 8080) — Ctrl+C pour arrêter.
set -eu
cd "$(dirname "$0")/.."
PORT="${1:-8080}"
mkdir -p data
chmod 777 data   # nginx (utilisateur du conteneur) doit pouvoir y écrire le journal des déclarations
docker build -q -t rails-semprun-dev . > /dev/null
exec docker run --rm --name rails-semprun -p "$PORT:80" \
  -v "$PWD/public:/var/www/rails/public:ro" \
  -v "$PWD/data:/var/www/rails/data" \
  -v "$PWD/deploy/nginx:/etc/nginx/conf.d:ro" \
  rails-semprun-dev
